package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// The LAN scan (PLAN.md D44): which machines are on the LAN, wired or Wi-Fi.
// Every two minutes PocketBase sends one UDP datagram to each address on --lan.
// To deliver them, the kernel asks for each address by ARP, and every machine
// on the LAN answers ARP, even one whose firewall drops everything else. The
// answers are read back from the ARP table into the machines collection; the
// online_machines view lists the ones that answered the latest scan.

const (
	lanScanSchedule = "*/2 * * * *"
	// At most a /20 is probed; on a bigger LAN only what the ARP table already
	// has is listed.
	lanScanMaxProbes = 4094
	lanScanBatch     = 32                     // datagrams at once, under net.inet.ip.arpq.maxlen
	lanScanPause     = 100 * time.Millisecond // between batches
	lanScanSettle    = 3 * time.Second        // for the ARP replies, after the last batch
	lanScanPort      = 9                      // discard
	// Machines not seen for this long are deleted.
	machineExpiry = 30 * 24 * time.Hour

	arpCommand = "/usr/sbin/arp"
	leasesFile = "/var/db/dhcpd.leases"
)

// A machine that answered a scan.
type sighting struct {
	mac      string // e.g. 02:00:5e:10:00:01
	ip       netip.Addr
	dnsName  string // this box's reverse name for ip, e.g. nas.<domain>
	hostname string // the name in its newest DHCP lease from this box
}

// What saving a scan changed.
type scanResult struct {
	online, new, offline, deleted int
}

var lanScanning sync.Mutex

// scheduleLANScan scans now and every two minutes after. The job is listed in
// the dashboard under Settings > Crons, where it can be run by hand too.
func scheduleLANScan(app core.App) {
	if probeTargets(lanPrefix, boxAddr) == nil {
		app.Logger().Warn("lan scan: the LAN is bigger than a /20, so it isn't probed; only machines already in the ARP table are listed",
			"lan", lanPrefix.String())
	}
	run := func() {
		if _, err := lanScan(app); err != nil {
			app.Logger().Error("lan scan failed", "error", err)
		}
	}
	app.Cron().MustAdd("lan_scan", lanScanSchedule, run)
	go run()
}

// lanScan scans the LAN once and saves what it found. Scans never overlap.
func lanScan(app core.App) (scanResult, error) {
	if !lanScanning.TryLock() {
		return scanResult{}, errors.New("the previous scan is still running")
	}
	defer lanScanning.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	seen, err := scanLAN(ctx)
	if err != nil {
		return scanResult{}, err
	}
	return recordScan(app, seen, time.Now())
}

// scanLAN probes the LAN and returns the machines that answered.
func scanLAN(ctx context.Context) ([]sighting, error) {
	if targets := probeTargets(lanPrefix, boxAddr); targets != nil {
		if err := probe(targets); err != nil {
			return nil, fmt.Errorf("probe: %w", err)
		}
		select {
		case <-time.After(lanScanSettle):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out, err := exec.CommandContext(ctx, arpCommand, "-an").Output()
	if err != nil {
		return nil, fmt.Errorf("%s -an: %w", arpCommand, err)
	}
	macs := parseARP(out, lanPrefix)
	// No file until dhcpd has run; the hostnames are only extras.
	leases, _ := os.ReadFile(leasesFile)
	hostnames := parseLeases(leases)

	ips := make([]netip.Addr, 0, len(macs))
	for _, ip := range macs {
		ips = append(ips, ip)
	}
	names := reverseNames(ctx, net.DefaultResolver, ips)
	seen := make([]sighting, 0, len(macs))
	for mac, ip := range macs {
		seen = append(seen, sighting{mac: mac, ip: ip, dnsName: names[ip], hostname: hostnames[mac]})
	}
	slices.SortFunc(seen, func(a, b sighting) int { return a.ip.Compare(b.ip) })
	return seen, nil
}

// probeTargets lists the addresses on prefix, less its network and broadcast
// addresses and this box; nil if there are more than lanScanMaxProbes.
func probeTargets(prefix netip.Prefix, box netip.Addr) []netip.Addr {
	n := 1<<(32-prefix.Bits()) - 2
	if n > lanScanMaxProbes {
		return nil
	}
	targets := make([]netip.Addr, 0, n)
	a := prefix.Masked().Addr()
	for range n {
		a = a.Next()
		if a != box {
			targets = append(targets, a)
		}
	}
	return targets
}

// probe sends a byte to each target's discard port, a batch at a time. Only
// the ARP request the kernel sends first matters, so send errors are ignored,
// e.g. EHOSTDOWN for an address that didn't answer ARP the time before.
func probe(targets []netip.Addr) error {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	for i, a := range targets {
		if i > 0 && i%lanScanBatch == 0 {
			time.Sleep(lanScanPause)
		}
		_, _ = conn.WriteToUDPAddrPort([]byte{0}, netip.AddrPortFrom(a, lanScanPort))
	}
	return nil
}

// parseARP reads arp -an output and returns, for each MAC that answered on
// prefix, the address it answered for most recently: a machine that moved
// keeps its old address's entry until that expires. Entries still waiting for
// an answer ("(incomplete)"), expired ones, and ones this box publishes for
// another machine are left out; this box's own address is kept.
func parseARP(out []byte, prefix netip.Prefix) map[string]netip.Addr {
	type entry struct {
		ip   netip.Addr
		left time.Duration // until the entry expires
	}
	best := map[string]entry{}
	for line := range strings.Lines(string(out)) {
		// Host, Ethernet Address, Netif, Expire, and Flags if any.
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		ip, err := netip.ParseAddr(f[0])
		if err != nil || !prefix.Contains(ip) {
			continue // the header, or another network
		}
		mac, ok := normalMAC(f[1])
		if !ok || (len(f) > 4 && strings.Contains(f[4], "p")) {
			continue
		}
		var left time.Duration
		switch f[3] {
		case "permanent":
			left = math.MaxInt64
		case "expired":
			continue // hasn't answered since
		default:
			left, _ = time.ParseDuration(f[3]) // e.g. 19m42s
		}
		if b, ok := best[mac]; !ok || left > b.left {
			best[mac] = entry{ip, left}
		}
	}
	macs := make(map[string]netip.Addr, len(best))
	for mac, e := range best {
		macs[mac] = e.ip
	}
	return macs
}

// normalMAC writes a MAC as six lowercase two-digit groups, e.g.
// 02:00:5e:10:00:01; false if s isn't one.
func normalMAC(s string) (string, bool) {
	groups := strings.Split(s, ":")
	if len(groups) != 6 {
		return "", false
	}
	for i, g := range groups {
		n, err := strconv.ParseUint(g, 16, 8)
		if err != nil || len(g) > 2 {
			return "", false
		}
		groups[i] = fmt.Sprintf("%02x", n)
	}
	return strings.Join(groups, ":"), true
}

// parseLeases reads dhcpd.leases and returns each MAC's client-hostname from
// its newest lease that has one. dhcpd doesn't keep the file in order, so
// "newest" goes by each lease's start time.
func parseLeases(data []byte) map[string]string {
	names := map[string]string{}
	newest := map[string]string{} // MAC -> start of the lease its name is from
	var starts, mac, hostname string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSuffix(strings.TrimSpace(line), ";")
		switch {
		case strings.HasPrefix(line, "lease "):
			starts, mac, hostname = "", "", ""
		case strings.HasPrefix(line, "starts "):
			// starts 0 2026/11/22 02:14:42 UTC: a weekday, then a time that sorts as text.
			if f := strings.Fields(line); len(f) >= 4 {
				starts = f[2] + " " + f[3]
			}
		case strings.HasPrefix(line, "hardware ethernet "):
			mac, _ = normalMAC(strings.TrimPrefix(line, "hardware ethernet "))
		case strings.HasPrefix(line, "client-hostname "):
			hostname = cleanHostname(strings.TrimPrefix(line, "client-hostname "))
		case line == "}":
			if mac != "" && hostname != "" && starts >= newest[mac] {
				newest[mac], names[mac] = starts, hostname
			}
		}
	}
	return names
}

// cleanHostname unquotes a name a device sent, and keeps it printable and
// within 255 bytes.
func cleanHostname(s string) string {
	s = strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	for len(s) > 255 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

type addrResolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// reverseNames looks up each address's name on this box's DNS, e.g.
// nas.<domain>: what the LAN sees, from hosts records and static.conf.
func reverseNames(ctx context.Context, r addrResolver, ips []netip.Addr) map[netip.Addr]string {
	var mu sync.Mutex
	var wg sync.WaitGroup
	names := map[netip.Addr]string{}
	for _, ip := range ips {
		wg.Go(func() {
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if n, err := r.LookupAddr(c, ip.String()); err == nil && len(n) > 0 {
				mu.Lock()
				names[ip] = strings.TrimSuffix(n[0], ".")
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return names
}

// recordScan saves a scan made at now: the machines that answered are online
// at the address they answered on, the others that were online aren't any
// more, and machines not seen for machineExpiry are deleted. Notes are never
// changed, and a hostname is kept once its lease is gone.
func recordScan(app core.App, seen []sighting, now time.Time) (scanResult, error) {
	var res scanResult
	stamp, err := types.ParseDateTime(now)
	if err != nil {
		return res, err
	}
	cutoff, err := types.ParseDateTime(now.Add(-machineExpiry))
	if err != nil {
		return res, err
	}
	err = app.RunInTransaction(func(txApp core.App) error {
		res = scanResult{}
		machines, err := txApp.FindCollectionByNameOrId("machines")
		if err != nil {
			return err
		}
		log := txApp.Logger()

		answered := map[string]bool{}
		for _, s := range seen {
			answered[s.mac] = true
			rec, err := txApp.FindFirstRecordByData(machines, "mac", s.mac)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				rec = core.NewRecord(machines)
				rec.Set("mac", s.mac)
				rec.Set("first_seen", stamp)
				res.new++
				log.Info("lan scan: new machine", "mac", s.mac, "ip", s.ip.String(), "name", s.dnsName, "hostname", s.hostname)
			case err != nil:
				return err
			case !rec.GetBool("online"):
				log.Info("lan scan: online", "mac", s.mac, "ip", s.ip.String(), "name", s.dnsName)
			}
			rec.Set("ipv4", s.ip.String())
			rec.Set("ip_order", ipOrder(s.ip))
			rec.Set("online", true)
			rec.Set("last_seen", stamp)
			rec.Set("dns_name", s.dnsName)
			if s.hostname != "" {
				rec.Set("hostname", s.hostname)
			}
			if err := txApp.Save(rec); err != nil {
				return err
			}
			res.online++
		}

		wasOnline, err := txApp.FindRecordsByFilter(machines, "online = true", "", 0, 0)
		if err != nil {
			return err
		}
		for _, rec := range wasOnline {
			if answered[rec.GetString("mac")] {
				continue
			}
			rec.Set("online", false)
			if err := txApp.Save(rec); err != nil {
				return err
			}
			res.offline++
			log.Info("lan scan: offline", "mac", rec.GetString("mac"), "ip", rec.GetString("ipv4"), "name", rec.GetString("dns_name"))
		}

		old, err := txApp.FindRecordsByFilter(machines, "last_seen < {:cutoff}", "", 0, 0,
			dbx.Params{"cutoff": cutoff.String()})
		if err != nil {
			return err
		}
		for _, rec := range old {
			if err := txApp.Delete(rec); err != nil {
				return err
			}
			res.deleted++
			log.Info("lan scan: deleted a machine not seen for 30 days", "mac", rec.GetString("mac"),
				"ip", rec.GetString("ipv4"), "last_seen", rec.GetString("last_seen"))
		}
		return nil
	})
	return res, err
}

// ipOrder is an IPv4 address as a number, so lists sort by address.
func ipOrder(ip netip.Addr) int {
	a := ip.As4()
	return int(binary.BigEndian.Uint32(a[:]))
}
