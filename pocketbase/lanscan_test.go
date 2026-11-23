package main

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// arp -an on the box, as OpenBSD prints it; made-up MACs.
const arpOutput = `Host                                 Ethernet Address    Netif Expire    Flags
172.16.0.6                           02:00:00:00:10:06     em1 permanent l
192.0.2.1                            02:00:00:00:00:01     em0 19m42s
192.0.2.20                           02:00:00:00:00:20     em0 26s
192.0.2.150                          02:00:00:00:01:50     em0 4m21s
192.0.2.160                          02:00:00:00:01:50     em0 1m2s
192.0.2.170                          (incomplete)          em0 expired
192.0.2.171                          02:00:00:00:01:71     em0 expired
192.0.2.180                          02:00:00:00:01:80     em0 permanent p
192.0.2.53                           02:00:00:00:00:53     em0 permanent l
192.0.2.200                          2:0:0:0:2:0           em0 1h0m5s
198.51.100.5                         02:00:00:00:09:05     em2 3m0s
`

func TestParseARP(t *testing.T) {
	got := parseARP([]byte(arpOutput), netip.MustParsePrefix("192.0.2.0/24"))
	want := map[string]string{
		"02:00:00:00:00:01": "192.0.2.1",
		"02:00:00:00:00:20": "192.0.2.20",
		"02:00:00:00:01:50": "192.0.2.150", // its newer entry; .160 is about to expire
		"02:00:00:00:00:53": "192.0.2.53",  // this box
		"02:00:00:00:02:00": "192.0.2.200",
	}
	if len(got) != len(want) {
		t.Errorf("got %d machines, want %d: %v", len(got), len(want), got)
	}
	for mac, ip := range want {
		if got[mac].String() != ip {
			t.Errorf("%s: got %v, want %s", mac, got[mac], ip)
		}
	}
}

func TestNormalMAC(t *testing.T) {
	for in, want := range map[string]string{
		"02:00:5e:10:00:01": "02:00:5e:10:00:01",
		"2:0:5E:10:0:1":     "02:00:5e:10:00:01",
	} {
		if got, ok := normalMAC(in); !ok || got != want {
			t.Errorf("normalMAC(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "(incomplete)", "02:00:5e:10:00", "02:00:5e:10:00:01:02", "02:00:5e:10:00:100", "02:00:5e:10:00:zz", "02-00-5e-10-00-01"} {
		if got, ok := normalMAC(bad); ok {
			t.Errorf("normalMAC(%q) = %q, want false", bad, got)
		}
	}
}

// dhcpd.leases, out of order as dhcpd keeps it; made-up MACs.
const leasesFileContent = `lease 192.0.2.140 {
	starts 0 2026/11/22 02:14:42 UTC;
	ends 1 2026/11/23 02:14:42 UTC;
	hardware ethernet 02:00:00:00:01:40;
	uid 01:02:00:00:00:01:40;
	client-hostname "laptop-new";
}
lease 192.0.2.141 {
	starts 6 2026/11/21 23:05:59 UTC;
	ends 0 2026/11/22 23:05:59 UTC;
	hardware ethernet 02:00:00:00:01:40;
	client-hostname "laptop-old";
}
lease 192.0.2.142 {
	starts 0 2026/11/22 05:00:00 UTC;
	ends 1 2026/11/23 05:00:00 UTC;
	hardware ethernet 02:00:00:00:01:40;
}
lease 192.0.2.150 {
	starts 0 2026/11/22 01:00:00 UTC;
	ends 1 2026/11/23 01:00:00 UTC;
	hardware ethernet 2:0:0:0:1:50;
	client-hostname "Jo's phone` + "\x07" + `";
}
lease 192.0.2.160 {
	starts 0 2026/11/22 01:00:00 UTC;
	ends 1 2026/11/23 01:00:00 UTC;
	hardware ethernet 02:00:00:00:01:60;
	abandoned;
}
`

func TestParseLeases(t *testing.T) {
	got := parseLeases([]byte(leasesFileContent))
	want := map[string]string{
		"02:00:00:00:01:40": "laptop-new", // the newest lease with a name
		"02:00:00:00:01:50": "Jo's phone",
	}
	if len(got) != len(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for mac, name := range want {
		if got[mac] != name {
			t.Errorf("%s: got %q, want %q", mac, got[mac], name)
		}
	}
	if len(parseLeases(nil)) != 0 {
		t.Error("no leases file gave names")
	}
	if long := cleanHostname(`"` + strings.Repeat("é", 200) + `"`); len(long) > 255 || !strings.HasPrefix(long, "é") {
		t.Errorf("cleanHostname kept %d bytes", len(long))
	}
}

func TestProbeTargets(t *testing.T) {
	box := netip.MustParseAddr("192.0.2.53")
	targets := probeTargets(netip.MustParsePrefix("192.0.2.0/24"), box)
	if len(targets) != 253 || targets[0].String() != "192.0.2.1" || targets[252].String() != "192.0.2.254" {
		t.Fatalf("/24: %d targets, %v to %v", len(targets), targets[0], targets[len(targets)-1])
	}
	for _, a := range targets {
		if a == box {
			t.Error("the box probes itself")
		}
	}
	if n := len(probeTargets(netip.MustParsePrefix("10.20.16.0/20"), netip.MustParseAddr("10.20.16.5"))); n != 4093 {
		t.Errorf("/20: %d targets, want 4093", n)
	}
	if targets := probeTargets(netip.MustParsePrefix("10.20.0.0/19"), netip.MustParseAddr("10.20.0.5")); targets != nil {
		t.Errorf("/19: %d targets, want none (too big to probe)", len(targets))
	}
	if targets := probeTargets(netip.MustParsePrefix("192.0.2.4/30"), netip.MustParseAddr("192.0.2.5")); len(targets) != 1 || targets[0].String() != "192.0.2.6" {
		t.Errorf("/30: %v", targets)
	}
}

type fakeResolver map[string]string

func (f fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if name, ok := f[addr]; ok {
		return []string{name}, nil
	}
	return nil, errors.New("no such host")
}

func TestReverseNames(t *testing.T) {
	r := fakeResolver{"192.0.2.20": "nas.example.lan.", "192.0.2.1": "router.example.lan."}
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.20"), netip.MustParseAddr("192.0.2.150")}
	got := reverseNames(context.Background(), r, ips)
	if len(got) != 2 || got[ips[0]] != "router.example.lan" || got[ips[1]] != "nas.example.lan" {
		t.Errorf("got %v", got)
	}
}

func machine(t *testing.T, app core.App, mac string) *core.Record {
	t.Helper()
	r, err := app.FindFirstRecordByData("machines", "mac", mac)
	if err != nil {
		t.Fatalf("machine %s: %v", mac, err)
	}
	return r
}

func scanAt(t *testing.T, app core.App, now time.Time, seen ...sighting) scanResult {
	t.Helper()
	res, err := recordScan(app, seen, now)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func onlineMACs(t *testing.T, app core.App) string {
	t.Helper()
	recs, err := app.FindAllRecords("online_machines")
	if err != nil {
		t.Fatal(err)
	}
	var macs []string
	for _, r := range recs {
		macs = append(macs, r.GetString("mac"))
	}
	return strings.Join(macs, " ")
}

func TestRecordScan(t *testing.T) {
	app := newTestApp(t)
	const nas, phone = "02:00:00:00:00:20", "02:00:00:00:01:50"
	t0 := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	ip := netip.MustParseAddr

	res := scanAt(t, app, t0,
		sighting{nas, ip("192.0.2.20"), "nas.example.lan", "nas"},
		sighting{phone, ip("192.0.2.150"), "", "phone"})
	if res != (scanResult{online: 2, new: 2}) {
		t.Errorf("first scan: %+v", res)
	}
	r := machine(t, app, nas)
	if r.GetString("ipv4") != "192.0.2.20" || r.GetInt("ip_order") != 0xc0000214 || !r.GetBool("online") ||
		r.GetString("dns_name") != "nas.example.lan" || r.GetString("hostname") != "nas" ||
		!r.GetDateTime("first_seen").Time().Equal(t0) || !r.GetDateTime("last_seen").Time().Equal(t0) {
		t.Fatalf("nas: %v", r.PublicExport())
	}
	if got := onlineMACs(t, app); got != nas+" "+phone {
		t.Errorf("online_machines: %s", got)
	}
	r.Set("notes", "the NAS in the hall")
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}

	// The NAS moved and has no name or lease now; the phone left.
	t1 := t0.Add(2 * time.Minute)
	if res := scanAt(t, app, t1, sighting{nas, ip("192.0.2.21"), "", ""}); res != (scanResult{online: 1, offline: 1}) {
		t.Errorf("second scan: %+v", res)
	}
	r = machine(t, app, nas)
	if r.GetString("ipv4") != "192.0.2.21" || r.GetString("dns_name") != "" || r.GetString("hostname") != "nas" ||
		r.GetString("notes") != "the NAS in the hall" || !r.GetDateTime("first_seen").Time().Equal(t0) ||
		!r.GetDateTime("last_seen").Time().Equal(t1) {
		t.Fatalf("nas after moving: %v", r.PublicExport())
	}
	if p := machine(t, app, phone); p.GetBool("online") || !p.GetDateTime("last_seen").Time().Equal(t0) {
		t.Fatalf("phone after leaving: %v", p.PublicExport())
	}
	if got := onlineMACs(t, app); got != nas {
		t.Errorf("online_machines: %s", got)
	}

	// 30 days after the NAS was last seen, the phone is back; the NAS goes a scan later.
	t2 := t1.Add(machineExpiry).Add(-time.Minute)
	if res := scanAt(t, app, t2, sighting{phone, ip("192.0.2.151"), "", ""}); res != (scanResult{online: 1, offline: 1}) {
		t.Errorf("third scan: %+v", res)
	}
	if p := machine(t, app, phone); !p.GetBool("online") || p.GetString("ipv4") != "192.0.2.151" || !p.GetDateTime("first_seen").Time().Equal(t0) {
		t.Fatalf("phone back: %v", p.PublicExport())
	}
	machine(t, app, nas)
	if res := scanAt(t, app, t2.Add(2*time.Minute), sighting{phone, ip("192.0.2.151"), "", ""}); res != (scanResult{online: 1, deleted: 1}) {
		t.Errorf("fourth scan: %+v", res)
	}
	if _, err := app.FindFirstRecordByData("machines", "mac", nas); err == nil {
		t.Error("the NAS wasn't deleted 30 days after it was last seen")
	}

	// Nothing answers, e.g. the box lost its link: everything goes offline.
	if res := scanAt(t, app, t2.Add(4*time.Minute)); res != (scanResult{offline: 1}) || onlineMACs(t, app) != "" {
		t.Errorf("empty scan: %+v, online %q", res, onlineMACs(t, app))
	}
}

func TestMachinesRefuseBadMACs(t *testing.T) {
	app := newTestApp(t)
	col, err := app.FindCollectionByNameOrId("machines")
	if err != nil {
		t.Fatal(err)
	}
	scanAt(t, app, time.Now(), sighting{mac: "02:00:00:00:00:20", ip: netip.MustParseAddr("192.0.2.20")})
	for _, mac := range []string{"", "02:00:00:00:00:20", "02:00:00:00:00:2", "02:00:00:00:00:2A"} {
		r := core.NewRecord(col)
		r.Set("mac", mac)
		if err := app.Save(r); err == nil {
			t.Errorf("machine with MAC %q was saved", mac)
		}
	}
}

func TestMachinesMigrationRevertsCleanly(t *testing.T) {
	app := newTestApp(t)
	addHost(t, app, "printer", "192.0.2.40", false)
	scanAt(t, app, time.Now(), sighting{mac: "02:00:00:00:00:40", ip: netip.MustParseAddr("192.0.2.40")})

	runner := core.NewMigrationsRunner(app, core.AppMigrations)
	n := migrationsFrom(t, "1761700000_machines.go")
	if reverted, err := runner.Down(n); err != nil || len(reverted) != n {
		t.Fatalf("down: %v %v", reverted, err)
	}
	for _, name := range []string{"machines", "online_machines"} {
		if _, err := app.FindCollectionByNameOrId(name); err == nil {
			t.Errorf("%s survived the down migration", name)
		}
	}
	if r := host(t, app, "printer"); r.GetString("ipv4") != "192.0.2.40" {
		t.Fatalf("printer changed: %v", r.PublicExport())
	}

	if applied, err := runner.Up(); err != nil || len(applied) != n {
		t.Fatalf("up: %v %v", applied, err)
	}
	scanAt(t, app, time.Now(), sighting{mac: "02:00:00:00:00:40", ip: netip.MustParseAddr("192.0.2.40")})
	if got := onlineMACs(t, app); got != "02:00:00:00:00:40" {
		t.Errorf("online_machines: %s", got)
	}
}
