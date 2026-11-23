package main

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/pocketbase/dbx"
	validation "github.com/pocketbase/ozzo-validation/v4"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// Device check-ins (PLAN.md §9). A device calls GET /api/hostupdate with Basic
// auth (its devices name and password); its hosts record gets the request's
// source address. Replies are one plain-text line, in the style of dyndns2.

// The LAN, from --lan and --gateway, which the installer fills in from the
// box's interface (D41); set by setLAN before serving.
var (
	lanPrefix netip.Prefix
	boxAddr   netip.Addr

	// Network, gateway, this box (static.conf) and broadcast.
	reservedIPs map[netip.Addr]bool
)

// setLAN takes this box's address with the LAN's prefix length, e.g.
// 192.0.2.53/24, and the LAN's gateway, e.g. 192.0.2.1.
func setLAN(lan, gateway string) error {
	box, err := netip.ParsePrefix(lan)
	if err != nil || !box.Addr().Is4() || box.Bits() < 8 || box.Bits() > 30 {
		return errors.New("--lan must be this box's IPv4 address and the LAN's prefix length, e.g. --lan=192.0.2.53/24")
	}
	network := box.Masked()
	a := network.Addr().As4()
	binary.BigEndian.PutUint32(a[:], binary.BigEndian.Uint32(a[:])|(1<<(32-box.Bits())-1))
	broadcast := netip.AddrFrom4(a)
	if box.Addr() == network.Addr() || box.Addr() == broadcast {
		return errors.New("--lan: this box's address can't be the LAN's network or broadcast address")
	}
	gw, err := netip.ParseAddr(gateway)
	if err != nil || !network.Contains(gw) || gw == box.Addr() || gw == network.Addr() || gw == broadcast {
		return errors.New("--gateway must be the router's address on the --lan network, e.g. --gateway=192.0.2.1")
	}
	lanPrefix, boxAddr = network, box.Addr()
	reservedIPs = map[netip.Addr]bool{network.Addr(): true, gw: true, box.Addr(): true, broadcast: true}
	return nil
}

// deviceExpiry matches dnssync's "last_seen >= @yesterday" filter.
const deviceExpiry = 24 * time.Hour

func hostUpdateHandler(e *core.RequestEvent) error {
	name, password, _ := e.Request.BasicAuth()
	status, reply := checkIn(e.App, name, password, e.RemoteIP())
	if status == http.StatusUnauthorized {
		e.Response.Header().Set("WWW-Authenticate", `Basic realm="hostupdate"`)
	}
	return e.String(status, reply+"\n")
}

// hostUpdateInfoHandler serves GET /api/hostupdate/info, which needs no login:
// the zones a device should send to this box (install.sh's DNS step, D32).
func hostUpdateInfoHandler(e *core.RequestEvent) error {
	return e.String(http.StatusOK, "domain="+domain+"\nreverse="+reverseZone(lanPrefix)+"\n")
}

// reverseZone names the in-addr.arpa zone of a prefix's whole octets, e.g.
// 2.0.192.in-addr.arpa for 192.0.2.0/24, 0.10.in-addr.arpa for 10.0.16.0/20.
func reverseZone(p netip.Prefix) string {
	octets := p.Addr().As4()
	zone := "in-addr.arpa"
	for _, o := range octets[:p.Bits()/8] {
		zone = strconv.Itoa(int(o)) + "." + zone
	}
	return zone
}

// checkIn points the device's hosts record at remoteIP and returns the HTTP
// status and reply line: good|nochg <ip>, badauth, badip <ip>,
// conflict <name>, or error.
func checkIn(app core.App, name, password, remoteIP string) (int, string) {
	device, err := app.FindFirstRecordByData("devices", "name", name)
	if err != nil || name == "" || device.GetBool("disabled") || !device.ValidatePassword(password) {
		return http.StatusUnauthorized, "badauth"
	}

	ip, err := netip.ParseAddr(remoteIP)
	ip = ip.Unmap()
	if err != nil || !lanPrefix.Contains(ip) || reservedIPs[ip] {
		return http.StatusBadRequest, "badip " + remoteIP
	}
	addr := ip.String()

	status, reply := http.StatusInternalServerError, "error"
	err = app.RunInTransaction(func(txApp core.App) error {
		hosts, err := txApp.FindCollectionByNameOrId("hosts")
		if err != nil {
			return err
		}

		changed := false
		rec, err := txApp.FindFirstRecordByData(hosts, "device", device.Id)
		if errors.Is(err, sql.ErrNoRows) {
			// Adopt a hand-made record with the device's name (D24), or start one.
			changed = true
			rec, err = txApp.FindFirstRecordByFilter(hosts, "name = {:name} && device = ''",
				dbx.Params{"name": name})
			if errors.Is(err, sql.ErrNoRows) {
				rec, err = core.NewRecord(hosts), nil
			}
		}
		if err != nil {
			return err
		}
		if rec.GetString("alias_of") != "" {
			// An alias's name follows another record; a device can't take it (D35).
			status, reply = http.StatusConflict, "conflict "+name
			txApp.Logger().Warn("hostupdate: name held by an alias", "device", name)
			return nil
		}

		// Hand-made records are never changed by devices: if one holds this IP
		// with a PTR, refuse rather than serve two owners for one address.
		owner, err := txApp.FindFirstRecordByFilter(hosts,
			"ipv4 = {:ip} && device = '' && no_ptr = false && disabled = false && id != {:id}",
			dbx.Params{"ip": addr, "id": rec.Id})
		if err == nil {
			status, reply = http.StatusConflict, "conflict "+owner.GetString("name")
			txApp.Logger().Warn("hostupdate: IP held by a hand-made record",
				"device", name, "ip", addr, "record", owner.GetString("name"))
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// The device proved it holds the IP, so other devices' records lose it.
		others, err := txApp.FindRecordsByFilter(hosts, "ipv4 = {:ip} && device != '' && id != {:id}",
			"", 0, 0, dbx.Params{"ip": addr, "id": rec.Id})
		if err != nil {
			return err
		}
		for _, other := range others {
			other.Set("ipv4", "")
			if err := txApp.Save(other); err != nil {
				return err
			}
			txApp.Logger().Info("hostupdate: IP taken over",
				"device", name, "ip", addr, "from", other.GetString("name"))
		}

		previous := rec.GetString("ipv4")
		if previous != addr || rec.GetString("name") != name ||
			rec.GetDateTime("last_seen").Time().Before(time.Now().Add(-deviceExpiry)) {
			changed = true
		}
		rec.Set("name", name)
		rec.Set("device", device.Id)
		rec.Set("ipv4", addr)
		rec.Set("last_seen", types.NowDateTime())
		if err := txApp.Save(rec); err != nil {
			var verrs validation.Errors
			if errors.As(err, &verrs) && verrs["name"] != nil {
				// Another device's record still has this name (e.g. after a rename).
				status, reply = http.StatusConflict, "conflict "+name
				return nil
			}
			return err
		}

		status, reply = http.StatusOK, "nochg "+addr
		if changed {
			reply = "good " + addr
			txApp.Logger().Info("hostupdate: check-in", "device", name, "ip", addr, "previous", previous)
		}
		return nil
	})
	if err != nil {
		app.Logger().Error("hostupdate: check-in failed", "device", name, "ip", addr, "error", err)
		return http.StatusInternalServerError, "error"
	}
	return status, reply
}
