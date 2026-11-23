package main

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

const pw = "0123456789abcdefghijklmnopqrstuv" // 32 characters, the devices minimum

// $FILTER and $ALIAS_FILTER in dnssync/dnssync (TestDnssyncFiltersMatchScript).
const (
	dnssyncFilter      = `disabled = false && ipv4 != "" && (device = "" || last_seen >= @yesterday)`
	dnssyncAliasFilter = `disabled = false && device = "" && ipv4 = ""`
)

func newTestApp(t *testing.T) *tests.TestApp {
	t.Helper()
	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	domain = "example.lan" // nothing may depend on home.arpa
	// Nor on any real LAN: these tests use 192.0.2.0/24, kept for documentation (RFC 5737).
	if err := setLAN("192.0.2.53/24", "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	registerHooks(app)
	return app
}

func addDevice(t *testing.T, app core.App, name string, disabled bool) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("devices")
	if err != nil {
		t.Fatal(err)
	}
	r := core.NewRecord(col)
	r.Set("name", name)
	r.Set("disabled", disabled)
	r.SetPassword(pw)
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func addHost(t *testing.T, app core.App, name, ipv4 string, noPTR bool) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId("hosts")
	if err != nil {
		t.Fatal(err)
	}
	r := core.NewRecord(col)
	r.Set("name", name)
	r.Set("ipv4", ipv4)
	r.Set("no_ptr", noPTR)
	r.Set("notes", "hand-made")
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func addAlias(t *testing.T, app core.App, name string, target *core.Record) *core.Record {
	t.Helper()
	r, err := newAlias(app, name, target)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newAlias(app core.App, name string, target *core.Record) (*core.Record, error) {
	col, err := app.FindCollectionByNameOrId("hosts")
	if err != nil {
		return nil, err
	}
	r := core.NewRecord(col)
	r.Set("name", name)
	r.Set("alias_of", target.Id)
	return r, app.Save(r)
}

func host(t *testing.T, app core.App, name string) *core.Record {
	t.Helper()
	r, err := app.FindFirstRecordByData("hosts", "name", name)
	if err != nil {
		t.Fatalf("host %q: %v", name, err)
	}
	return r
}

func expect(t *testing.T, app core.App, name, password, ip string, wantStatus int, wantReply string) {
	t.Helper()
	status, reply := checkIn(app, name, password, ip)
	if status != wantStatus || reply != wantReply {
		t.Fatalf("checkIn(%s, %s) = %d %q, want %d %q", name, ip, status, reply, wantStatus, wantReply)
	}
}

func TestCheckInCreatesAndUpdates(t *testing.T) {
	app := newTestApp(t)
	dev := addDevice(t, app, "nas", false)

	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")
	r := host(t, app, "nas")
	if r.GetString("device") != dev.Id || r.GetString("ipv4") != "192.0.2.150" || r.GetDateTime("last_seen").IsZero() {
		t.Fatalf("record not set up: %v", r.PublicExport())
	}

	expect(t, app, "nas", pw, "192.0.2.150", 200, "nochg 192.0.2.150")
	expect(t, app, "nas", pw, "192.0.2.151", 200, "good 192.0.2.151")
	// RemoteIP() reports an IPv4 client on a dual-stack listener as 4in6.
	expect(t, app, "nas", pw, "0000:0000:0000:0000:0000:ffff:c000:0297", 200, "nochg 192.0.2.151")
}

func TestCheckInRejectsBadAuth(t *testing.T) {
	app := newTestApp(t)
	addDevice(t, app, "nas", false)
	addDevice(t, app, "old", true)

	expect(t, app, "nas", pw+"x", "192.0.2.150", 401, "badauth")
	expect(t, app, "nobody", pw, "192.0.2.150", 401, "badauth")
	expect(t, app, "old", pw, "192.0.2.150", 401, "badauth")
	expect(t, app, "", "", "192.0.2.150", 401, "badauth")
}

func TestCheckInRejectsBadIP(t *testing.T) {
	app := newTestApp(t)
	addDevice(t, app, "nas", false)

	for _, ip := range []string{"10.0.0.5", "198.51.100.10", "192.0.2.0", "192.0.2.1",
		"192.0.2.53", "192.0.2.255", "fe80::1", "garbage", ""} {
		expect(t, app, "nas", pw, ip, 400, "badip "+ip)
	}
	if _, err := app.FindFirstRecordByData("hosts", "name", "nas"); err == nil {
		t.Fatal("bad IP check-in created a record")
	}
}

func TestCheckInAdoptsHandMadeRecord(t *testing.T) {
	app := newTestApp(t)
	manual := addHost(t, app, "nas", "192.0.2.40", true)
	dev := addDevice(t, app, "nas", false)

	expect(t, app, "nas", pw, "192.0.2.40", 200, "good 192.0.2.40")
	r := host(t, app, "nas")
	if r.Id != manual.Id || r.GetString("device") != dev.Id || r.GetString("notes") != "hand-made" || !r.GetBool("no_ptr") {
		t.Fatalf("not adopted in place: %v", r.PublicExport())
	}
}

func TestCheckInTakesIPFromOtherDevice(t *testing.T) {
	app := newTestApp(t)
	addDevice(t, app, "laptop", false)
	addDevice(t, app, "pi", false)

	expect(t, app, "laptop", pw, "192.0.2.150", 200, "good 192.0.2.150")
	expect(t, app, "pi", pw, "192.0.2.150", 200, "good 192.0.2.150")
	if ip := host(t, app, "laptop").GetString("ipv4"); ip != "" {
		t.Fatalf("laptop kept %q", ip)
	}
	expect(t, app, "laptop", pw, "192.0.2.151", 200, "good 192.0.2.151")
}

func TestCheckInNeverChangesHandMadeRecords(t *testing.T) {
	app := newTestApp(t)
	addHost(t, app, "printer", "192.0.2.150", false)
	addHost(t, app, "media", "192.0.2.151", true) // alias without PTR: allowed to share
	addDevice(t, app, "nas", false)

	expect(t, app, "nas", pw, "192.0.2.150", 409, "conflict printer")
	if _, err := app.FindFirstRecordByData("hosts", "name", "nas"); err == nil {
		t.Fatal("refused check-in created a record")
	}
	expect(t, app, "nas", pw, "192.0.2.151", 200, "good 192.0.2.151")
	if host(t, app, "printer").GetString("ipv4") != "192.0.2.150" || host(t, app, "media").GetString("ipv4") != "192.0.2.151" {
		t.Fatal("hand-made record changed")
	}
}

func TestCheckInAfterExpiryIsGood(t *testing.T) {
	app := newTestApp(t)
	addDevice(t, app, "nas", false)
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")

	r := host(t, app, "nas")
	old, _ := types.ParseDateTime(time.Now().Add(-25 * time.Hour))
	r.Set("last_seen", old)
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")
}

func TestCheckInRenamedDevice(t *testing.T) {
	app := newTestApp(t)
	dev := addDevice(t, app, "nas", false)
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")

	dev.Set("name", "storage")
	if err := app.Save(dev); err != nil {
		t.Fatal(err)
	}
	addDevice(t, app, "nas", false) // reuses the old name before "storage" checks in
	expect(t, app, "nas", pw, "192.0.2.152", 409, "conflict nas")
	expect(t, app, "storage", pw, "192.0.2.150", 200, "good 192.0.2.150")
	expect(t, app, "nas", pw, "192.0.2.152", 200, "good 192.0.2.152")
}

func TestValidationHooks(t *testing.T) {
	app := newTestApp(t)
	hosts, _ := app.FindCollectionByNameOrId("hosts")
	devices, _ := app.FindCollectionByNameOrId("devices")

	noIP := core.NewRecord(hosts)
	noIP.Set("name", "printer")
	if err := app.Save(noIP); err == nil {
		t.Fatal("hand-made record without ipv4 was saved")
	}

	reservedHost := core.NewRecord(hosts)
	reservedHost.Set("name", "dns")
	reservedHost.Set("ipv4", "192.0.2.40")
	if err := app.Save(reservedHost); err == nil {
		t.Fatal("reserved host name was saved")
	}

	reservedDevice := core.NewRecord(devices)
	reservedDevice.Set("name", "router")
	reservedDevice.SetPassword(pw)
	if err := app.Save(reservedDevice); err == nil {
		t.Fatal("reserved device name was saved")
	}

	shortPassword := core.NewRecord(devices)
	shortPassword.Set("name", "nas")
	shortPassword.SetPassword("short-password-1234")
	if err := app.Save(shortPassword); err == nil {
		t.Fatal("device with a password under 32 characters was saved")
	}
}

func TestDeletingDeviceDeletesItsRecord(t *testing.T) {
	app := newTestApp(t)
	dev := addDevice(t, app, "nas", false)
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")

	if err := app.Delete(dev); err != nil {
		t.Fatal(err)
	}
	if _, err := app.FindFirstRecordByData("hosts", "name", "nas"); err == nil {
		t.Fatal("host record survived its device")
	}
}

func TestDnssyncFilter(t *testing.T) {
	app := newTestApp(t)
	addHost(t, app, "printer", "192.0.2.40", false)
	disabled := addHost(t, app, "off", "192.0.2.41", false)
	disabled.Set("disabled", true)
	if err := app.Save(disabled); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fresh", "stale", "released"} {
		addDevice(t, app, name, false)
	}
	expect(t, app, "fresh", pw, "192.0.2.150", 200, "good 192.0.2.150")
	expect(t, app, "stale", pw, "192.0.2.151", 200, "good 192.0.2.151")
	expect(t, app, "released", pw, "192.0.2.152", 200, "good 192.0.2.152")
	expect(t, app, "fresh", pw, "192.0.2.152", 200, "good 192.0.2.152") // releases "released"

	stale := host(t, app, "stale")
	old, _ := types.ParseDateTime(time.Now().Add(-25 * time.Hour))
	stale.Set("last_seen", old)
	if err := app.Save(stale); err != nil {
		t.Fatal(err)
	}

	recs, err := app.FindRecordsByFilter("hosts", dnssyncFilter, "name", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.GetString("name"))
	}
	if len(got) != 2 || got[0] != "fresh" || got[1] != "printer" {
		t.Fatalf("dnssync would serve %v, want [fresh printer]", got)
	}
}

func TestHostUpdateHandler(t *testing.T) {
	app := newTestApp(t)
	addDevice(t, app, "nas", false)

	call := func(auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/hostupdate", nil)
		req.RemoteAddr = "192.0.2.150:40000"
		if auth {
			req.SetBasicAuth("nas", pw)
		}
		rec := httptest.NewRecorder()
		e := &core.RequestEvent{App: app}
		e.Request, e.Response = req, rec
		if err := hostUpdateHandler(e); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	if rec := call(true); rec.Code != 200 || rec.Body.String() != "good 192.0.2.150\n" {
		t.Fatalf("authed: %d %q", rec.Code, rec.Body.String())
	}
	rec := call(false)
	if rec.Code != 401 || rec.Body.String() != "badauth\n" || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no auth: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
}

// migrationsFrom counts the app's migrations from file on, the number to undo
// to get to before it; tests that do keep working as migrations are added.
func migrationsFrom(t *testing.T, file string) int {
	t.Helper()
	for i, m := range core.AppMigrations.Items() {
		if m.File == file {
			return len(core.AppMigrations.Items()) - i
		}
	}
	t.Fatalf("no migration %s", file)
	return 0
}

// The box already has hosts records from the init migration; check that the
// later migrations keep them, and that they revert cleanly.
func TestDevicesMigrationKeepsExistingRecords(t *testing.T) {
	app := newTestApp(t)
	runner := core.NewMigrationsRunner(app, core.AppMigrations)
	n := migrationsFrom(t, "1761400000_devices.go")
	if reverted, err := runner.Down(n); err != nil || len(reverted) != n {
		t.Fatalf("down: %v %v", reverted, err)
	}
	if _, err := app.FindCollectionByNameOrId("devices"); err == nil {
		t.Fatal("devices survived the down migration")
	}

	addHost(t, app, "printer", "192.0.2.40", false)
	addHost(t, app, "media", "192.0.2.40", true)

	if applied, err := runner.Up(); err != nil || len(applied) != n {
		t.Fatalf("up: %v %v", applied, err)
	}
	if r := host(t, app, "printer"); r.GetString("ipv4") != "192.0.2.40" || r.GetString("device") != "" {
		t.Fatalf("printer changed: %v", r.PublicExport())
	}
	addDevice(t, app, "media", false)
	expect(t, app, "media", pw, "192.0.2.150", 200, "good 192.0.2.150")
}

// The box has device and hand-made records when the aliases migration runs.
func TestAliasesMigrationRevertsCleanly(t *testing.T) {
	app := newTestApp(t)
	printer := addHost(t, app, "printer", "192.0.2.40", false)
	addHost(t, app, "web.test", "192.0.2.41", false)
	addAlias(t, app, "app", printer)

	runner := core.NewMigrationsRunner(app, core.AppMigrations)
	n := migrationsFrom(t, "1761500000_aliases.go")
	if reverted, err := runner.Down(n); err != nil || len(reverted) != n {
		t.Fatalf("down: %v %v", reverted, err)
	}
	hosts, _ := app.FindCollectionByNameOrId("hosts")
	if hosts.Fields.GetByName("alias_of") != nil {
		t.Fatal("alias_of survived the down migration")
	}
	if _, err := app.FindFirstRecordByData("hosts", "name", "app"); err == nil {
		t.Fatal("alias survived the down migration")
	}
	if r := host(t, app, "printer"); r.GetString("ipv4") != "192.0.2.40" {
		t.Fatalf("printer changed: %v", r.PublicExport())
	}

	if applied, err := runner.Up(); err != nil || len(applied) != n {
		t.Fatalf("up: %v %v", applied, err)
	}
	addAlias(t, app, "app", host(t, app, "printer"))
}

func TestDottedNames(t *testing.T) {
	app := newTestApp(t)
	label63 := strings.Repeat("a", 63)
	for _, name := range []string{"runtask.test.app", "test.app", "a.b-c.d", label63 + "." + label63} {
		addHost(t, app, name, "192.0.2.40", true)
	}
	// 241 characters + ".example.lan" is DNS's 253.
	longest := strings.Repeat(label63+".", 3) + strings.Repeat("a", 241-64*3)
	addHost(t, app, longest, "192.0.2.40", true)

	hosts, _ := app.FindCollectionByNameOrId("hosts")
	for _, name := range []string{"a..b", ".app", "app.", "-a.b", "a.-b", "A.b", "a_b.c",
		label63 + "a.b", longest + "a", "www.router", "x.dns", "dns", "a.pocketbase"} {
		r := core.NewRecord(hosts)
		r.Set("name", name)
		r.Set("ipv4", "192.0.2.41")
		r.Set("no_ptr", true)
		if err := app.Save(r); err == nil {
			t.Errorf("host name %q was saved", name)
		}
	}
	// Device names stay one label: install.sh and the client use them as-is.
	devices, _ := app.FindCollectionByNameOrId("devices")
	d := core.NewRecord(devices)
	d.Set("name", "test.app")
	d.SetPassword(pw)
	if err := app.Save(d); err == nil {
		t.Error("dotted device name was saved")
	}
}

func TestAliasValidation(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	other := addHost(t, app, "other", "192.0.2.41", false)
	alias := addAlias(t, app, "api.app", server)

	withIP, _ := newAlias(app, "x", server)
	withIP.Set("ipv4", "192.0.2.42")
	if err := app.Save(withIP); err == nil {
		t.Error("alias with its own ipv4 was saved")
	}
	if _, err := newAlias(app, "chain", alias); err == nil {
		t.Error("alias of an alias was saved")
	}
	if _, err := newAlias(app, "api.app", other); err == nil {
		t.Error("duplicate alias name was saved")
	}

	// A target can't become an alias, and an alias can't point at itself.
	server.Set("ipv4", "")
	server.Set("alias_of", other.Id)
	if err := app.Save(server); err == nil {
		t.Error("record with aliases became an alias")
	}
	alias.Set("alias_of", alias.Id)
	if err := app.Save(alias); err == nil {
		t.Error("alias of itself was saved")
	}

	addDevice(t, app, "nas", false)
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")
	owned := host(t, app, "nas")
	owned.Set("ipv4", "")
	owned.Set("alias_of", other.Id)
	if err := app.Save(owned); err == nil {
		t.Error("device record became an alias")
	}

	// Re-pointing an alias at another record is fine.
	alias = host(t, app, "api.app")
	alias.Set("alias_of", host(t, app, "nas").Id)
	if err := app.Save(alias); err != nil {
		t.Fatal(err)
	}
}

func TestDeletingTargetDarkensAliases(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	addAlias(t, app, "app", server)
	dev := addDevice(t, app, "nas", false)
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")
	addAlias(t, app, "files", host(t, app, "nas"))

	if err := app.Delete(server); err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(dev); err != nil { // cascades to the nas hosts record
		t.Fatal(err)
	}
	for _, name := range []string{"app", "files"} {
		if r := host(t, app, name); r.GetString("alias_of") != "" {
			t.Errorf("%s still points at %q", name, r.GetString("alias_of"))
		}
	}

	// A dark alias must get a target or an address before it can be saved.
	if err := app.Save(host(t, app, "app")); err == nil {
		t.Error("alias without a target or ipv4 was saved")
	}
}

func takePTR(app core.App, alias *core.Record) error {
	alias.Set("takes_ptr", true)
	return app.Save(alias)
}

func TestTakesPTRValidation(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	other := addHost(t, app, "other", "192.0.2.41", false)
	quiet := addHost(t, app, "quiet", "192.0.2.42", true)
	api := addAlias(t, app, "api.app", server)
	web := addAlias(t, app, "web.app", server)
	own := addAlias(t, app, "own", other)

	if err := takePTR(app, api); err != nil {
		t.Fatal(err)
	}
	if err := app.Save(api); err != nil { // re-saving the one that takes it
		t.Fatal(err)
	}
	if err := takePTR(app, web); err == nil || !strings.Contains(err.Error(), "Already taken: api.app takes") {
		t.Errorf("a second alias took server's PTR, or no clear message: %v", err)
	}
	if err := app.SaveNoValidate(web); err == nil {
		t.Error("idx_hosts_takes_ptr let a second alias take server's PTR")
	}
	if err := takePTR(app, own); err != nil {
		t.Fatal(err)
	}
	own.Set("alias_of", server.Id)
	if err := app.Save(own); err == nil || !strings.Contains(err.Error(), "Already taken: api.app takes") {
		t.Errorf("an alias taking a PTR was re-pointed at a record whose PTR is taken: %v", err)
	}
	if err := takePTR(app, addAlias(t, app, "q", quiet)); err == nil {
		t.Error("an alias took the PTR of a no_ptr record")
	}

	if err := takePTR(app, host(t, app, "other")); err == nil {
		t.Error("a hand-made record with takes_ptr was saved")
	}
	addDevice(t, app, "nas", false)
	expect(t, app, "nas", pw, "192.0.2.150", 200, "good 192.0.2.150")
	if err := takePTR(app, host(t, app, "nas")); err == nil {
		t.Error("a device record with takes_ptr was saved")
	}
	// A check-in leaves the device's alias, and the PTR it takes, alone.
	files := addAlias(t, app, "files", host(t, app, "nas"))
	if err := takePTR(app, files); err != nil {
		t.Fatal(err)
	}
	expect(t, app, "nas", pw, "192.0.2.151", 200, "good 192.0.2.151")
	if r := host(t, app, "files"); !r.GetBool("takes_ptr") || r.GetString("alias_of") != host(t, app, "nas").Id {
		t.Fatalf("files changed: %v", r.PublicExport())
	}
}

// Deleting the target empties alias_of (D34); the takes_ptr tick stays and
// applies again once the alias points at a record whose PTR is free.
func TestDeletingTargetKeepsTakesPTR(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	other := addHost(t, app, "other", "192.0.2.41", false)
	api := addAlias(t, app, "api.app", server)
	addAlias(t, app, "web.app", server)
	if err := takePTR(app, api); err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(server); err != nil {
		t.Fatal(err)
	}
	api = host(t, app, "api.app")
	if api.GetString("alias_of") != "" || !api.GetBool("takes_ptr") {
		t.Fatalf("api.app after deleting its target: %v", api.PublicExport())
	}
	api.Set("alias_of", other.Id)
	if err := app.Save(api); err != nil {
		t.Fatal(err)
	}
}

// The box has aliases, one taking a PTR, when the migration runs.
func TestAliasPTRMigrationRevertsCleanly(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	if err := takePTR(app, addAlias(t, app, "api.app", server)); err != nil {
		t.Fatal(err)
	}
	addAlias(t, app, "web.app", server)

	runner := core.NewMigrationsRunner(app, core.AppMigrations)
	n := migrationsFrom(t, "1761600000_alias_ptr.go")
	if reverted, err := runner.Down(n); err != nil || len(reverted) != n {
		t.Fatalf("down: %v %v", reverted, err)
	}
	hosts, _ := app.FindCollectionByNameOrId("hosts")
	if hosts.Fields.GetByName("takes_ptr") != nil || hosts.GetIndex("idx_hosts_takes_ptr") != "" {
		t.Fatal("takes_ptr survived the down migration")
	}
	for _, name := range []string{"api.app", "web.app"} {
		if r := host(t, app, name); r.GetString("alias_of") != server.Id {
			t.Fatalf("%s changed: %v", name, r.PublicExport())
		}
	}

	if applied, err := runner.Up(); err != nil || len(applied) != n {
		t.Fatalf("up: %v %v", applied, err)
	}
	if host(t, app, "api.app").GetBool("takes_ptr") {
		t.Error("takes_ptr came back ticked")
	}
	if err := takePTR(app, host(t, app, "web.app")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckInRefusesAliasName(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	alias := addAlias(t, app, "app", server)
	addDevice(t, app, "app", false)

	expect(t, app, "app", pw, "192.0.2.150", 409, "conflict app")
	if r := host(t, app, "app"); r.Id != alias.Id || r.GetString("alias_of") != server.Id || r.GetString("device") != "" {
		t.Fatalf("alias changed: %v", r.PublicExport())
	}
}

func TestDnssyncAliasFilter(t *testing.T) {
	app := newTestApp(t)
	server := addHost(t, app, "server", "192.0.2.40", false)
	gone := addHost(t, app, "gone", "192.0.2.41", false)
	addAlias(t, app, "app", server)
	off := addAlias(t, app, "off", server)
	off.Set("disabled", true)
	if err := app.Save(off); err != nil {
		t.Fatal(err)
	}
	addAlias(t, app, "dark", gone)
	if err := app.Delete(gone); err != nil {
		t.Fatal(err)
	}
	addDevice(t, app, "laptop", false)
	addDevice(t, app, "pi", false)
	expect(t, app, "laptop", pw, "192.0.2.150", 200, "good 192.0.2.150")
	expect(t, app, "pi", pw, "192.0.2.150", 200, "good 192.0.2.150") // releases laptop's IP

	served := names(t, app, dnssyncFilter)
	if strings.Join(served, " ") != "pi server" {
		t.Errorf("$FILTER gives %v, want [pi server]", served)
	}
	aliases := names(t, app, dnssyncAliasFilter)
	if strings.Join(aliases, " ") != "app dark" {
		t.Errorf("$ALIAS_FILTER gives %v, want [app dark]", aliases)
	}
}

func names(t *testing.T, app core.App, filter string) []string {
	t.Helper()
	recs, err := app.FindRecordsByFilter("hosts", filter, "name", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.GetString("name"))
	}
	return got
}

func TestDnssyncFiltersMatchScript(t *testing.T) {
	script, err := os.ReadFile("../dnssync/dnssync")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"my $FILTER = '" + dnssyncFilter + "';",
		"my $ALIAS_FILTER = '" + dnssyncAliasFilter + "';",
	} {
		if !strings.Contains(string(script), line) {
			t.Errorf("dnssync/dnssync lacks %s", line)
		}
	}
}

func TestHostUpdateInfoHandler(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/hostupdate/info", nil)
	rec := httptest.NewRecorder()
	e := &core.RequestEvent{App: app}
	e.Request, e.Response = req, rec
	if err := hostUpdateInfoHandler(e); err != nil {
		t.Fatal(err)
	}
	if want := "domain=example.lan\nreverse=2.0.192.in-addr.arpa\n"; rec.Code != 200 || rec.Body.String() != want {
		t.Fatalf("%d %q, want %q", rec.Code, rec.Body.String(), want)
	}
}

func TestReverseZone(t *testing.T) {
	for prefix, want := range map[string]string{
		"192.0.2.0/24": "2.0.192.in-addr.arpa",
		"10.20.0.0/16": "20.10.in-addr.arpa",
		"10.0.0.0/8":   "10.in-addr.arpa",
	} {
		if got := reverseZone(netip.MustParsePrefix(prefix)); got != want {
			t.Errorf("reverseZone(%s) = %q, want %q", prefix, got, want)
		}
	}
}

func TestSetLAN(t *testing.T) {
	app := newTestApp(t)
	addDevice(t, app, "nas", false)
	if err := setLAN("10.20.30.40/16", "10.20.0.254"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = setLAN("192.0.2.53/24", "192.0.2.1") })
	if lanPrefix.String() != "10.20.0.0/16" || reverseZone(lanPrefix) != "20.10.in-addr.arpa" {
		t.Fatalf("lan %s, reverse zone %s", lanPrefix, reverseZone(lanPrefix))
	}
	expect(t, app, "nas", pw, "10.20.200.7", 200, "good 10.20.200.7")
	for _, ip := range []string{"10.20.0.0", "10.20.0.254", "10.20.30.40", "10.20.255.255", "10.21.0.1", "192.0.2.150"} {
		expect(t, app, "nas", pw, ip, 400, "badip "+ip)
	}

	for _, bad := range [][2]string{
		{"", "10.20.0.254"},                 // no --lan
		{"10.20.30.40", "10.20.0.254"},      // no prefix length
		{"10.20.0.0/16", "10.20.0.254"},     // the network address
		{"10.20.255.255/16", "10.20.0.254"}, // the broadcast address
		{"10.20.30.40/31", "10.20.30.41"},   // too small for a LAN
		{"fd00::5/64", "fd00::1"},           // not IPv4
		{"10.20.30.40/16", ""},              // no --gateway
		{"10.20.30.40/16", "10.21.0.1"},     // gateway off the LAN
		{"10.20.30.40/16", "10.20.30.40"},   // gateway is the box
		{"10.20.30.40/16", "10.20.255.255"}, // gateway is the broadcast address
	} {
		if err := setLAN(bad[0], bad[1]); err == nil {
			t.Errorf("setLAN(%q, %q) succeeded", bad[0], bad[1])
		}
	}
}
