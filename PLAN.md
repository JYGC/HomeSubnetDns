# Home Subnet DNS Server — Plan

Target: an OpenBSD 8.0 amd64 host on the home LAN (a VM, here), reached over
SSH as an admin user in group `wheel`.

Status: **deployed on 2026-10-24.** Phases 1–9 are complete; see §5a for where
the build differs from the plan, and §8 for what's still open.

The examples use the domain `home.arpa` and the documentation addresses of
RFC 5737: the box at 192.0.2.53 on 192.0.2.0/24, the router at 192.0.2.1.
Since D41 nothing in the repo names a real network: a site's own values are
in its `site.conf`, which git ignores.

## 1. Starting point (read-only survey, 2026-10-23)

| Item | Finding |
|---|---|
| OS / hardware | OpenBSD 8.0 GENERIC.MP amd64, 2 CPU, ~1 GB RAM, 27 GB free on `/var`; a virtio NIC, so a VM |
| Network | An address by DHCP (`dhcpleased`) from the router, IPv6 via SLAAC |
| Gateway / current DNS | The router (also advertised as IPv6 DNS via RA) |
| DNS software | `unbound` 1.26.1 and `nsd` in base; neither enabled; nothing on port 53; `unbound.conf` is stock (localhost only) |
| Firewall | `pf` enabled; `/etc/pf.conf` is root-only (not yet reviewed) |
| Resolver on host | `resolvd` manages `/etc/resolv.conf` (points at router) |
| Running services | cron, dhcpleased, ntpd, pf, pflogd, resolvd, slaacd, smtpd, sndiod, sshd, syslogd |
| Access | SSH key login works for the admin user (member of `wheel`); privileged steps need `doas` |
| PocketBase | Not in OpenBSD packages; no upstream OpenBSD binary. Pure Go → cross-compile on the workstation (Go 1.26.7) |

## 2. Design summary

```
 LAN clients ──DHCP──▶ dhcpd @ 192.0.2.53   (gateway 192.0.2.1, DNS 192.0.2.53, domain home.arpa)
     │
     └──DNS───▶ unbound @ 192.0.2.53 ──▶ root/TLD/authoritative servers
                  │  recursive, DNSSEC-validating, caching
                  │  home.arpa: static.conf + pocketbase.conf
                  └── pocketbase.conf ◀── dnssync (cron, every minute, user _dnssync)
                                            ▲ reads REST API
                      PocketBase :8090 (user _pocketbase) ◀── admin UI from LAN

 Router: the gateway only, with its DHCP server turned off. It has no DNS
 setting that works (D8).
```

- **Unbound** (base) does full recursion with DNSSEC validation, and serves `home.arpa`.
- **PocketBase** holds hostname → IPv4 records, edited through its web admin UI.
- **dnssync**, a Perl script that uses only modules shipped with OpenBSD, runs
  every minute as `_dnssync`. It renders the records into an Unbound include
  file, validates it, and reloads Unbound without flushing the cache.
- **dhcpd** (in OpenBSD base) on the box replaces the router's DHCP server and
  hands out the box as the only DNS server.
- All config is kept in this git repo and deployed by the installer (D43).

## 3. Decisions

| ID | Topic | Decision |
|---|---|---|
| D1 | Local domain | `home.arpa` (RFC 8375). Since D31 it's `DOMAIN` in `site.conf` |
| D2 | Upstream resolution | Full recursion from the root servers |
| D3 | Ad/tracker blocking | Off for now |
| D4 | IPv6 service | IPv4 only; no ULA on the box. **Revised 2026-10-24:** clients started using the router's IPv6 DNS (its RA sets M/O, so it's handed out by DHCPv6), and `home.arpa` failed (job 21). Fix: stop the router handing out IPv6 DNS (e.g. turn its IPv6 off), plus a Windows NRPT rule for the domain and the reverse zone, and hostupdate's DNS step on other devices (D26–D28) |
| D5 | Local hostnames source | PocketBase on the box, synced by a cron job as `_dnssync`, a member of group `_unbound` (see §5 Phase 7 for the trade-off) |
| D6 | Server IP addressing | Static IP on host, outside the router's DHCP pool |
| D7 | Host's own resolver | Disable `resolvd`; `/etc/resolv.conf` → `127.0.0.1` |
| D8 | Client rollout | **Revised 2026-10-24:** the router's only DNS setting has no effect (tested). Instead, **dhcpd runs on the box** and the router's DHCP server is turned off |
| D9 | Redundancy | **Revised:** clients get the box as their **only** DNS server. A second resolver can be added later |
| D10 | Privileged execution | `expect` over `ssh -tt` answering the doas prompt |
| D11 | Config management | This directory under git; deployed by the installer (D43) |
| D12 | PocketBase admin UI | Reachable from the LAN subnet only, plain HTTP |
| D13 | PocketBase backups | Built-in nightly backup, kept on the box only. **Revised 2026-10-26:** see D38 |
| D14 | Device check-ins: auth | One account per device in a new `devices` auth collection, created by the admin and tied to one name (§9) |
| D15 | Device check-ins: IP | The request's source address (`RemoteIP`, not proxy headers), which must be inside the LAN subnet |
| D16 | Device check-ins: stale | A device record that hasn't checked in for **24 h** isn't served. A check-in takes its IP over from other *device* records |
| D17 | Device client | POSIX `sh`, using curl → GNU wget → OpenBSD `ftp(1)` → BusyBox wget |
| D18 | Device transport | Plain HTTP (as D12): `GET` with Basic auth, plain-text reply |
| D19 | Password in `ps` | Prefer curl/GNU wget, which keep it out of argv. Falling back to `ftp(1)`/BusyBox wget exposes it to local users for the length of the request; accepted |
| D20 | Device schedule | cron **hourly** at a random minute, plus `@reboot` with retries. A systemd timer only where there's no cron |
| D21 | Device run-as | A dedicated system user, `_hostupdate` (OpenBSD) or `hostupdate` (Linux); no root needed |
| D22 | Device install | `hostupdate/install.sh` from this repo, run as root on the device |
| D23 | Names per device | Exactly one |
| D24 | Existing record with the name | The first check-in adopts the hand-made record and keeps its notes, no_ptr and disabled settings |
| D25 | Device password | The installer generates 32 random alphanumerics and shows them once. The admin pastes them into the `devices` record |
| D26 | Device DNS (2026-10-24) | `install.sh` also points `home.arpa` at the box by default; `--no-dns` skips it, and `--uninstall` reverts it using `/etc/hostupdate.state`. On Linux: systemd-resolved → routing-domain drop-in; NetworkManager → `ipv6.ignore-auto-dns` on the LAN connection |
| D27 | Plain-resolv.conf Linux | **Revised 2026-10-24:** dhcpcd gets `nooption nd_rdnss` and `nooption dhcp6_name_servers`, so it ignores the router's IPv6 DNS; this is safe off this LAN. dhclient, udhcpc and static setups get nothing, because the kernel's SLAAC ignores router DNS. Only if the router's IPv6 DNS can reach resolv.conf (an IPv6 nameserver is listed, or `rdnssd`, `dhclient -6` or `udhcpc6` runs) is the box pinned as the only nameserver |
| D28 | OpenBSD device DNS | unwind with `force forwarder { home.arpa <reverse zone> }`. Skipped if `unwind.conf` has its own forwarders, or resolvd is off |
| D29 | Installer re-runs | Idempotent: it keeps the name, password, extra config lines and cron minute. Files are written, and services restarted, only on change. Asks for the PocketBase record only while check-ins fail |
| D30 | Dotted names (2026-10-25) | `hosts` names may have several labels, e.g. `runtask.test.app`, for hand-made and alias records. Each label as before (≤ 63, lowercase, digits, inner hyphens), the whole `<name>.<domain>` ≤ 253. Names under a reserved one (`x.router`) are refused. Device names stay one label |
| D31 | Site domain | One setting, `DOMAIN` in `site.conf`. `scripts/stage` fills `@DOMAIN@` into the Unbound, dhcpd, dnssync and PocketBase rc.d files; PocketBase gets `--dns-domain`. Job 22 applies a change. The network addresses stay hard-coded (revised by D41) |
| D32 | Domain on devices | Public `GET /api/hostupdate/info` returns `domain=` and `reverse=` (no secrets). The installer asks it through `hostupdate --info` and saves both in `/etc/hostupdate.conf`; if the box doesn't answer it uses the saved value or `--domain`, else skips only the DNS step |
| D33 | Aliases | A `hosts` record with `alias_of` (a link to another hosts record) and no `ipv4`. dnssync serves it as an A record with the target's address, only while the target is served. It has no PTR of its own (but see D36). The target must hold an address itself: no chains |
| D34 | Alias target deleted | The alias stays, with `alias_of` emptied (PocketBase's behaviour for an optional link), and isn't served until it's pointed elsewhere; dnssync logs it as skipped |
| D35 | Check-in vs alias name | A device whose name belongs to an alias gets `409 conflict <name>`; nothing changes. Hand-made records with an address are still adopted (D24) |
| D36 | Alias takes the PTR (2026-10-26) | `takes_ptr` on an alias: the target's PTR gives the alias's name instead, while the alias is served; otherwise the target keeps its own. One per target (hook, plus a partial unique index). Refused on non-aliases and on targets with `no_ptr`; ticking `no_ptr` on the target later wins. For programs that turn an address back into a name: `getfqdn()`-style self-lookups and host-name access rules (`pg_hba.conf`, MySQL grants) |
| D37 | Rebuild installer (2026-10-26) | `scripts/install-box` → job 23 sets up a box from a fresh OpenBSD install in one idempotent pass (jobs 01–17's steps): it changes only what differs, `--check` only reports, and it stops for the person after the address switch, a kernel patch, and before dhcpd starts (`--start-dhcpd`) |
| D38 | Off-box backups | `scripts/backup` → job 24 stops PocketBase for a few seconds, copies `pb_data` (minus its `backups/`) and the DHCP leases, and the workstation keeps the file outside the repo. `--restore` puts it back on a box with no data yet, then sets a new dnssync password |
| D39 | Installer bundle | `scripts/bundle` packs job 23 (as `install-box/install.sh`), the PocketBase build and the rendered files (`install-box/files`, made by `scripts/stage --to`) into one tar file. Job 23 uses `files/` next to itself when it's there, else the stage folder, and prints its re-run commands to suit. `scripts/backup` refreshes the bundle |
| D40 | Device user's home (2026-11-02) | `/` on Linux, not `/nonexistent`. cronie won't start a job if the user's home is missing, so on Fedora every scheduled check-in failed, while `su`, which the installer's test uses, only warned. `/` exists everywhere and the user can't write to it. A re-run changes a missing home to `/`, leaves any other alone, and writes the crontab again, because cron keeps the old home until it re-reads it. Uninstall must never use `userdel -r` |
| D41 | Network read from the box (2026-11-19) | Nothing in the repo names the network. `site.conf` has `ADDRESS`, the box's LAN address, next to `DOMAIN`. The deployable files are templates; the installer fills them in on the box: netmask from the LAN interface, gateway from `--gateway`, else the default route, the DHCP lease or `/etc/mygate`, and the subnet, dhcpd pool (the upper half of the subnet, less the box and gateway) and reverse zone worked out from those. PocketBase takes `--lan=ADDRESS/BITS --gateway=…`. Devices take the box's address once (`install.sh --box`) and save it. Why: the LAN moved twice in a month (the router renumbered itself after a restart, then a new router brought another subnet), and each time every hard-coded copy of the address and subnet broke DNS |
| D42 | Moving the box (2026-11-19) | A new `ADDRESS` is applied by the installer like any other change. If the interface doesn't have it, `netswitch.sh` switches in the background and reverts to the previous settings (DHCP or another static address) unless the next run confirms within 3 minutes. Then devices re-run `install.sh --box` |
| D43 | The installer is the deploy path (2026-11-19) | Job 23 deploys every change, not only a rebuild; jobs 01, 04–09, 13, 14, 17, 19, 20 and 22 are gone (they installed staged files as they were, which are now templates). It's idempotent in effect, not only per file: it restarts Unbound or PocketBase when they run but don't answer on `ADDRESS`, fixes the live default route, and shows a diff for each file it would change. OS patches come last, so a kernel patch's reboot doesn't hold up the services |
| D44 | Machines on the LAN (2026-11-22) | PocketBase scans the LAN itself every 2 minutes (`lanscan.go`): a 1-byte UDP datagram to each address on `--lan`, so the kernel ARPs for each one, then `arp -an`. Every machine on the LAN, wired or Wi-Fi, answers ARP even when its firewall drops everything else. Results go into `machines`, one record per MAC, with the reverse name from the box's DNS and the hostname from dhcpd's leases, deleted 30 days after last seen; the `online_machines` view lists the ones that answered the latest scan. The installer sets `net.inet.ip.arptimeout=300` (from 1200), so a machine that leaves drops off within about 7 minutes. No root, package, user or service account. Not nmap or arp-scan from root's cron (root, a package, and a second writer to PocketBase), not a dnssync-style script (a user, crontab and service account for what PocketBase already knows), not DHCP leases alone (they say who got an address, not who's on) |

## 4. Repository layout (D11)

```
HomeSubnetDns/
├── PLAN.md, RUNBOOK.md, CLAUDE.md
├── site.conf.example           # copied to site.conf (not in git): DOMAIN, ADDRESS, BOX_USER
├── unbound/
│   ├── unbound.conf            # → /var/unbound/etc/unbound.conf
│   └── static.conf             # → /var/unbound/etc/home.d/static.conf
├── pf/pf.conf                  # → /etc/pf.conf
├── dhcpd/dhcpd.conf            # → /etc/dhcpd.conf
├── net/hostname.if, mygate     # → /etc/hostname.<if>, /etc/mygate
├── pocketbase/
│   ├── main.go, hostupdate.go, lanscan.go, *_test.go, go.mod   # pinned PocketBase version
│   ├── migrations/             # schema, backup cron, rate limits
│   └── rc.d/pocketbase         # → /etc/rc.d/pocketbase
├── dnssync/
│   ├── dnssync                 # → /usr/local/libexec/dnssync
│   └── crontab                 # → crontab -u _dnssync
├── hostupdate/                 # the device client and its installer (§9)
├── jobs/NN-*.sh                # run as root on the box; 23 is the installer
└── scripts/                    # build, stage, install-box, bundle, backup, rdoas (D10)
```

## 5. Implementation phases

The phases as planned. The files they describe are now templates in the
repo, which the installer renders and installs (D41, D43); the addresses
below are the examples'.

### Phase 1: Prepare the host
1. Back up `/etc/hostname.<if>`, `/etc/pf.conf`, `/var/unbound/etc/unbound.conf`
   and `/etc/resolv.conf` to `*.orig` on the box (the installer now does this).
2. `syspatch`; reboot if a kernel patch was applied.
3. Static addressing (D6): `/etc/hostname.<if>` with `inet 192.0.2.53
   255.255.255.0` and `inet6 autoconf`; `/etc/mygate` with the router's
   address. Keep the address out of the router's DHCP pool. Apply with
   `sh /etc/netstart <if>`, with a timed revert as a safety net (now
   `jobs/netswitch.sh`, D42).

### Phase 2: Unbound
1. `/var/unbound/etc/unbound.conf` (`unbound/unbound.conf`): listen on
   127.0.0.1, ::1 and the box's address; refuse everything but localhost and
   the LAN subnet; `hide-identity`, `hide-version`, `qname-minimisation`,
   `prefetch`, `aggressive-nsec`; DNSSEC from
   `auto-trust-anchor-file: "/var/unbound/db/root.key"`; DNS-rebinding
   protection with `private-address` for the private ranges and
   `private-domain` for the site domain; `include:
   "/var/unbound/etc/home.d/*.conf"`; `remote-control` on a Unix socket.
2. `/var/unbound/etc/home.d/static.conf` (root-owned, never touched by
   dnssync): the `local-zone` for the domain, and the `dns`, `pocketbase` and
   `router` names with their PTRs. Seed an empty
   `/var/unbound/etc/home.d/pocketbase.conf`, so Unbound starts before the
   first sync has run.
3. `unbound-anchor -a /var/unbound/db/root.key`. OpenBSD's rc script doesn't run it.
4. `unbound-checkconf` → `rcctl enable unbound` → `rcctl start unbound`.

### Phase 3: Firewall
1. Review the current `/etc/pf.conf`, then pass ports 53 (UDP and TCP) and
   8090 (TCP, D12) in from the LAN subnet only. If the base ruleset passes
   everything inbound, add an explicit block for ports 53 and 8090 from
   anywhere else. Confirm the SSH rule is unaffected.
2. `pfctl -nf /etc/pf.conf` (syntax check) → `pfctl -f /etc/pf.conf`.

### Phase 4: The host uses its own resolver (D7)
`rcctl disable resolvd && rcctl stop resolvd`, then write `/etc/resolv.conf`:
```
nameserver 127.0.0.1
lookup file bind
```

### Phase 5: Verify Unbound (on the host and from the workstation)
- `dig @192.0.2.53 openbsd.org` resolves; a repeat query takes about 0 ms (cached).
- `dig @192.0.2.53 dnssec-failed.org` returns `SERVFAIL`; a signed domain queried with `+dnssec` shows the `ad` flag.
- `dig @192.0.2.53 dns.home.arpa` and `dig @192.0.2.53 -x 192.0.2.53` both answer.
- `dig @192.0.2.53 openbsd.org +tcp` works.
- `unbound-control stats_noreset` runs; `/var/log/daemon` has no errors.

### Phase 6: PocketBase
1. **Build** on the workstation from `pocketbase/main.go` (pinned version):
   `CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 go build -o pocketbase`.
   Smoke-test it on the box (`./pocketbase --help`) before installing.
2. **Install:** binary at `/usr/local/bin/pocketbase`; user `_pocketbase`
   (no login shell); data dir `/var/pocketbase/pb_data` (owned by `_pocketbase`, mode 0700).
3. **Service:** `/etc/rc.d/pocketbase` (`pocketbase/rc.d/pocketbase`), as
   `_pocketbase`, with `serve --http=<ADDRESS>:8090 --dir=/var/pocketbase/pb_data`;
   `rc_bg=YES`, `rc_reload=NO`. `rcctl enable pocketbase`, then `rcctl start pocketbase`.
4. **Superuser:** created by the person in a browser, from the setup link
   PocketBase logs. The credentials aren't stored in the repo.
5. **Schema** (a migration in the repo):
   - Collection `hosts`:
     - `name`: text, required, pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
     - `ipv4`: text, required, IPv4 pattern
     - `ptr`: bool, default true
     - `enabled`: bool, default true
     - `notes`: text
   - Indexes: `UNIQUE(name)`, and a partial index
     `UNIQUE(ipv4) WHERE ptr = TRUE`, so each IP has at most one reverse name.
   - Reserved names, which dnssync skips: `dns`, `pocketbase`, `router`.
   - Auth collection `service_accounts` with one record, `dnssync`. The list/view
     rules on `hosts` allow only that account, read-only; superusers bypass the rules.
6. **Backups (D13):** turn on PocketBase's built-in backups (Settings → Backups),
   with cron `0 3 * * *` and the last 7 kept, in `/var/pocketbase/pb_data/backups`.
   They're on the box only, so they protect against bad edits, not against losing the VM.

### Phase 7: dnssync (D5)
1. **User:** `_dnssync`, with no login shell and secondary group `_unbound`.
   The group lets it use `/var/run/unbound.sock` (`_unbound`, mode 0660); verify
   the socket's owner and mode on the box.
2. **Paths:** `/var/unbound/etc/home.d/` is owned by `root:_unbound`, mode 0770, so
   `_dnssync` can create files there. `pocketbase.conf` is owned by
   `_dnssync:_unbound`, mode 0640, because Unbound re-reads includes as `_unbound`
   inside its chroot when it reloads. Credentials live in `~_dnssync/.pbcreds`, mode 0600.
3. **Script** `/usr/local/libexec/dnssync`, Perl using only modules shipped with
   OpenBSD (`HTTP::Tiny`, `JSON::PP`, `OpenBSD::Pledge`/`OpenBSD::Unveil`):
   1. Authenticate as `service_accounts/dnssync`, then fetch `hosts?filter=enabled=true`, paging through all results.
   2. Validate each record again (hostname and IPv4 syntax, reserved names, duplicates); log and skip bad rows.
   3. Render `local-data` (A) and `local-data-ptr` lines, sorted, into a temp file in `home.d/`.
   4. If the content hash equals the current `pocketbase.conf`, delete the temp file and exit.
   5. Run `unbound-checkconf` on the full config with the candidate file in place. On failure, keep the last working file, log an error, and exit.
   6. Atomically `rename` the temp file to `pocketbase.conf`, then run `unbound-control reload_keep_cache`.
   7. Log a summary to syslog (`logger -t dnssync`).
4. **Cron:** `crontab -u _dnssync`, with the entry `* * * * * /usr/local/libexec/dnssync`.
5. **Trade-off of D5:** membership in `_unbound` gives `_dnssync` full
   `unbound-control` access (stop, flush, cache dump, record injection). That's
   acceptable because `_dnssync` is a no-login service account whose only input
   is the read-only PocketBase API.

### Phase 8: Verify the sync end to end
- Add `test1` → `192.0.2.250` in the admin UI. Within about 60 s,
  `dig @192.0.2.53 test1.home.arpa` and `dig -x 192.0.2.250` both answer.
- Edit the record, then delete it; each change shows up in DNS.
- A duplicate name or a bad IP is rejected by PocketBase. If one gets through,
  dnssync skips it and logs it, and DNS keeps working.
- `rcctl stop pocketbase`: existing names still resolve and dnssync logs that
  the fetch failed. `rcctl start pocketbase` restores sync.
- Reboot the box: Unbound, PocketBase and the sync all come back by themselves.

### Phase 9: Network rollout with dhcpd on the box (D8/D9, revised)
Findings about the router at the time: its DHCP page had the LAN IP, the DHCP
on/off switch, the range, the lease time (1 day) and an IP/MAC binding list,
but no DNS fields. Its only DNS setting was on its WAN page, and a test showed
it had **no effect**: with it set to the box, the router still answered from
the ISP's DNS and sent no queries to the box.

Steps:
1. `/etc/dhcpd.conf` (in the repo as `dhcpd/dhcpd.conf`): option
   `domain-name "home.arpa"`, `domain-name-servers` the box, `routers` the
   router, a pool that leaves out the box's address, 1-day leases. Validate
   with `dhcpd -n`. `rcctl enable dhcpd`, `rcctl set dhcpd flags <if>`.
   Don't start it yet.
2. **Cutover.** Two DHCP servers must never run at the same time:
   (a) the person turns off *DHCP server* on the router, then (b) dhcpd starts right away.
3. Clients move over as their leases renew (within about 1 day). Until then
   they keep using the router's DNS, so nothing breaks in the meantime.
4. Verify: a reconnected client gets the box as its DNS server and resolves
   `test1.home.arpa`. Leases show up in `/var/db/dhcpd.leases`.
   **Result (2026-10-24):** the router's DHCP was turned off and dhcpd
   started. A reconnected phone got an address from the box, and the router
   made no offer; the phone's DNS queries went to the box. dhcpd ping-checks
   an address before offering it, so it skipped one still held by a device
   with a router-issued lease.
5. Trade-offs:
   - New devices can't join while the box is down; existing leases last 1 day.
   - Devices with PocketBase records need **fixed IPs**: either a static IP set
     on the device, or `host` entries in dhcpd.conf. Possible follow-up: add a
     `mac` field in PocketBase and have dnssync generate the dhcpd host entries
     too.
   - IPv6 bypass (D4): dual-stack clients may still ask the router's IPv6 DNS
     and miss `home.arpa` names.

### Phase 10: Operations
- Patching: run `syspatch` regularly; it covers Unbound. PocketBase upgrades are
  manual: bump the version in `go.mod`, rebuild, read the changelog, then redeploy.
- Logs: Unbound and PocketBase write to `/var/log/daemon`; dnssync writes to syslog with tag `dnssync`.
- Redundancy (D9): the box is the only DNS server, so an outage of it means
  no IPv4 DNS on the LAN. A second resolver can be added later.

## 5a. Execution notes (2026-10-24): where the build differs from the plan
- **Schema:** PocketBase bool fields can't have a default value of true, so the
  plan's `ptr` and `enabled` became **`no_ptr`** and **`disabled`**. Both
  default to false, which is "on". The unique-PTR index is
  `UNIQUE(ipv4) WHERE no_ptr = FALSE AND disabled = FALSE`.
- **Migrations** are Go code compiled into the binary
  (`pocketbase/migrations/`), so there's no `--migrationsDir`. The same
  migration also sets the backup cron (`0 3 * * *`, keep 7), turns on rate
  limiting, and sets the app name.
- **Reserved names** (`dns`, `pocketbase`, `router`) are rejected in the
  admin UI by an `OnRecordValidate` hook. dnssync also reads `static.conf` and
  skips them.
- **Service account:** created by a custom `pocketbase service-account EMAIL`
  command that reads the password from stdin. The password is random, generated
  on the box, and stored only in `/var/dnssync/.pbcreds` (mode 0600). The email
  is only an identifier: since 2026-11-22 the installer uses
  `dnssync@dns.invalid` (`.invalid` never resolves, RFC 2606). Data restored
  from before then keeps its older account, unused once the installer has
  made this one.
- **Validation:** dnssync installs the new file, runs `unbound-checkconf`, and
  restores `.pocketbase.prev` if Unbound rejects it. The temp and backup files
  are dotfiles, so the `*.conf` include doesn't pick them up.
- **TTL** of generated records is 60 s, so edits get through the router's cache quickly.
- **Privilege drop in jobs:** `su -s /bin/sh <user>` instead of `doas -u`,
  because doas asks root for a password.
- **The box was renamed to `dns`** (2026-10-24): the hostname is
  `dns.home.arpa` (`/etc/myname`), and `static.conf` serves it with its
  reverse name. dnssync now logs skipped records only when the generated file
  changes. Earlier, a record that stayed skipped produced a warning every minute.
- **Static-IP switch** ran as a detached `at` job that would revert to DHCP
  unless confirmed within 180 s. It was confirmed.

## 6. Open items to verify during execution
All verified on 2026-10-24:
- `pf.conf` was the stock "pass all", so explicit block rules were added for ports 53 and 8090.
- The Unbound control socket is `_unbound:_unbound`, mode 0660, as D5 needed.
- `unbound-checkconf` runs fine as `_dnssync`.
- PocketBase v0.40.6, cross-compiled with Go 1.27, runs on OpenBSD 8.0.
- Router: no DNS setting that works, so D8 was revised to dhcpd on the box (Phase 9).

## 7. Rollback
1. Restore the `*.orig` files (`hostname.<if>`, `pf.conf`, `unbound.conf`, `resolv.conf`, `dhcpd.conf`).
2. `rcctl disable pocketbase unbound`, then `rcctl stop pocketbase unbound`; `crontab -r -u _dnssync`.
3. `rcctl enable resolvd`, then `rcctl start resolvd`.
4. `sh /etc/netstart <if>`; `pfctl -f /etc/pf.conf`.
5. `rcctl disable dhcpd`, then `rcctl stop dhcpd`. **Then** turn the router's
   DHCP server back on.
6. Optional: remove the `_pocketbase` and `_dnssync` users, `/var/pocketbase` and `/usr/local/{bin,libexec}` files.
7. Remove the two HomeSubnetDns lines from `/etc/sysctl.conf` (D44), then
   `sysctl net.inet.ip.arptimeout=1200`.

## 8. Still open
- Optional:
  - have dnssync generate dhcpd `host` entries from the MACs in `machines`
    (D44), so devices with names get fixed IPs automatically
  - show each machine's maker in `machines`, from the MAC (an OUI list on the box)
  - revisit the IPv6 DNS bypass (D4) if clients misbehave, or if the router
    starts handing out an IPv6 DNS server
  - add a second resolver (D9)
- Not run yet: the address switch (D42) on a live box, and the installer on a
  fresh box.

## 9. Device check-ins (D14–D25): deployed 2026-10-24

Devices on the LAN run a small client from cron that tells PocketBase "my name
is X". PocketBase records the request's source IP, and dnssync serves it within
a minute.

```
 device (OpenBSD/Linux)                         dns.home.arpa
 cron: hourly + @reboot                          PocketBase :8090
 /usr/local/libexec/hostupdate ──GET /api/hostupdate (Basic nas:pw)──▶ devices (auth) ─┐
   curl | GNU wget | ftp(1) | busybox wget  ◀── "good 192.0.2.123" / "nochg …"           │
                                                 hosts.ipv4 = source IP, last_seen = now ◀┘
                                                       │ dnssync (every minute)
                                                       ▼
                                                 unbound: nas.home.arpa A/PTR
```

### 9.1 Server: PocketBase (new migration + `main.go`)
- **New `devices` auth collection.** Superuser-managed, with no API rules, and
  password auth turned off so the standard auth endpoints can't hand out tokens.
  - `name`: unique, the same hostname pattern as hosts, not a reserved name
  - `disabled`: bool
  - password: minimum 32 characters
  - email: optional
- **`hosts` changes:**
  - add `device` (relation to `devices`, max 1, unique when set, cascade delete)
  - add `last_seen` (date)
  - make `ipv4` optional in the schema; a hook still requires it for hand-made
    records (`device` empty)
  - add `` AND `ipv4` != '' `` to `idx_hosts_ptr_ipv4`
  - deleting a device account deletes its record; to suspend one, tick `disabled`
- **`GET /api/hostupdate`** with Basic auth. It answers in plain text, with
  dyndns2-style words, and the HTTP status shown:
  1. Find the device by name. If it's missing or disabled, or the password is
     wrong, reply `401 badauth` with `WWW-Authenticate: Basic`.
  2. Take `ip = e.RemoteIP()`. It must be in the LAN subnet and must not be
     its network, gateway, box or broadcast address; otherwise reply `400 badip`.
  3. In one transaction:
     - **Find the record**, by `device = account`. If none has that, look for
       `name = account.name` with no device, and adopt it (D24). If neither
       exists, create one.
     - **Check for a conflict.** An active hand-made record holding `ip` with
       a PTR means `409 conflict <name>`; hand-made records are never changed.
       Hand-made `no_ptr` aliases on the same IP are fine.
     - **Release the IP** from any other device record that holds it (set its
       `ipv4 = ''`).
     - **Update this record:** set `ipv4 = ip` and `last_seen = now`.
  4. Reply `200 good <ip>` if the IP changed or the record is new, otherwise
     `200 nochg <ip>`. Log `good` and `conflict` to the PocketBase log.
- **Rate limiting.** The endpoint is covered by the existing `/api/` rate
  limit. The random 32-character passwords make brute force pointless.

### 9.2 Server: dnssync
- **Filter in the API query.** The filter becomes `disabled = false && ipv4 != ""
  && (device = "" || last_seen >= "<now − 24 h, UTC>")`, so expired and released
  records simply drop out. They aren't logged as skips.
- **No other changes.** Unbound, pf (8090 is already open to the LAN) and
  dhcpd stay as they are.

### 9.3 Client: `hostupdate/hostupdate` (POSIX sh)
- **Config.** It reads `/etc/hostupdate.conf` (`name=`, `password=`, and
  `url=`, which `install.sh --box` writes since D41; the first version had
  the box's address built in). The file is parsed, not sourced. It's mode
  0400, owned by the run-as user.
- **HTTP client, in order (D17/D19):**
  1. `curl -K -`, with `user = "name:pw"` fed on stdin by the `printf` builtin
  2. GNU wget, with `--config` pointing to a umask-077 temp file holding
     `http_user`/`http_password`, plus `--auth-no-challenge` and
     `--content-on-error`
  3. OpenBSD `ftp -o - http://name:pw@…`
  4. BusyBox `wget -O - http://name:pw@…`
- **Logging.** It logs through `logger -t hostupdate` on `good`, on errors, and
  on `conflict`, and stays quiet on `nochg`. The exit status is non-zero on any
  failure.
- **`--boot`.** This mode retries every 15 s for up to 5 min until the server
  answers, for the `@reboot` entry.

### 9.4 Client: `hostupdate/install.sh` (run as root on the device)
- **Usage:** `install.sh [NAME]`, where NAME defaults to the short hostname in
  lowercase, and `install.sh --uninstall`.
- **What it does:**
  1. **Detects OpenBSD or Linux.**
  2. **Creates the user.** On OpenBSD that's `_hostupdate`, with `useradd -d /var/empty -s /sbin/nologin
     -g =uid`, as the box's own service users are made. On Linux it's
     `hostupdate`, with `useradd --system`, or `adduser -S` on BusyBox, and
     home `/` (D40).
  3. **Installs the client** as `/usr/local/libexec/hostupdate`.
  4. **Writes the config.** It generates the password with
     `tr -dc A-Za-z0-9 </dev/urandom | fold -w 32 | head -n 1` (OpenBSD's
     `head` has no `-c`) and writes `/etc/hostupdate.conf`. A re-run keeps the
     existing password.
  5. **Installs the crontab** with `crontab -u`: `M * * * *` at a random minute,
     plus `@reboot … --boot`. If there's no cron, it installs a
     `hostupdate.service` with a `.timer` that runs `OnBootSec=1min` and
     `OnUnitActiveSec=1h`.
  6. **Shows the name and password once**, with instructions to create the
     `devices` record at http://pocketbase.home.arpa:8090/_/. When you press
     Enter, it runs one check-in and prints the result.

### 9.5 Rollout
1. **Write the code:** the migration and endpoint, plus Go tests for the
   endpoint's rules; dnssync; the client and installer. Run
   `sh -n` and shellcheck on the scripts.
2. **Build, stage, deploy.** A new job installs the PocketBase binary and
   dnssync, then restarts PocketBase. DNS keeps working while it's down;
   verified earlier.
3. **Test from WSL.** WSL is NATed, so the check-in arrives from the Windows
   PC's LAN IP. Use a throwaway `devices` record, and test the curl, GNU wget
   and BusyBox paths.
4. **Test `ftp(1)` on the box.** Run the client as the admin user there.
   Expect `badip`, since the source is the box's own address, which still
   proves that auth works over `ftp(1)`.
5. **Check DNS:**
   - forward and reverse lookups work
   - the 24 h expiry works (set `last_seen` back by hand)
   - an IP is taken over from another device record
   - adoption of a hand-made record works
   - a conflict with a hand-made record is refused
6. **Write it up.** Add RUNBOOK sections for operating it and adding a device,
   and a CLAUDE.md line. Delete the test records.

### 9.6 As built (2026-10-24)
- **Code:**
  - `pocketbase/hostupdate.go` (the endpoint), `pocketbase/main.go` (hooks)
  - `pocketbase/migrations/1761400000_devices.go`
  - `dnssync/dnssync` (`$FILTER`)
  - `hostupdate/hostupdate`, `hostupdate/install.sh`
  - a deploy job (job 20, since replaced by the installer, D43)
- **Deployed with that job.** The migration applied, and the served
  `pocketbase.conf` was byte-for-byte unchanged. The job kept a copy of the
  pre-migration data on the box; later deploys copy to `pb_data.prev`.
- **Tests:**
  - **Go:** `go test ./...` in `pocketbase/`, 13 tests. They cover every
    rule in RUNBOOK §8a, the dnssync filter, and the migration's down/up with
    existing records.
  - **End to end:** against a throwaway PocketBase on the box (port 18090,
    deleted afterwards):
    - from WSL, curl `good`, then GNU wget and BusyBox wget `nochg`, with a
      wrong password giving `badauth` on all three
    - on the box, `ftp(1)` giving `badip` (its own address is reserved) and
      `badauth`
    - a stub server returning 400, 401, 409 and 500 to each Linux client
  - **`install.sh`:** smoke-tested with stubbed system commands (the cron and
    systemd paths, a re-run, bad names, uninstall).
  - **First real device:** a Debian machine, installed by the person. Its
    name resolved to its address, the reverse lookup gave its name, and the
    TTL was 60.
  - **Second device:** a laptop with a second, virtual interface on the LAN
    (a locally administered MAC). It checked in as that interface's address,
    because that's the one it uses to reach the box; both answer from the
    LAN. The person chose to keep it that way rather than add a `source=`
    option to the client.
- **Where the build differs from §9.1–9.4:**
  - **No dummy bcrypt compare** for unknown names. Device names are public
    anyway, since they're DNS names.
  - **`ftp(1)` puts the URL, password included, in its error messages**, so the
    client only ever takes the HTTP status code from stderr.
  - **GNU wget prints no body on a 401**, so the client maps its exit
    status 6 to `badauth`.
  - **`HOSTUPDATE_CLIENT` forces one HTTP tool**, for debugging.
  - **Renaming a device account renames its record** on the next check-in. If
    another device's record still has the new name, the reply is
    `409 conflict <name>`.

### 9.6a Installer DNS and idempotency (D26–D29, 2026-10-24)
- **The harness.** Tested in a stubbed fake root (scratch harness, not in the
  repo). It covers the systemd-resolved, NetworkManager, dhcpcd, dhclient,
  udhcpc, static, unknown, unwind, merged-unwind, unwind-with-forwarders and
  resolvd-off cases, plus the systemd timer, an upgrade from the first
  installer, `--no-dns`, a change of network setup between runs, and a rename.
- **Results.** In every case:
  - the re-run left the files byte-for-byte unchanged and restarted nothing
  - `--uninstall` restored the starting files
- **unwind.** The real `unwind -n` on the box accepted the merged
  `unwind.conf`.
- **Bugs the harness found, now fixed:**
  - `revert_nm` overwrote `apply_nm`'s variables.
  - `--uninstall` could copy the saved resolv.conf over one that
    systemd-resolved had since taken over.
- **D27 revision.** Same day, dhcpcd moved from pinning resolv.conf to the
  `nooption` lines, and the other plain setups to "pin only if needed". The
  harness grew to 19 scenarios, including an upgrade from the pinned dhcpcd
  setup, clean dhclient, udhcpc and static setups, and `dhclient -6`. All
  were idempotent on re-run, and every uninstall restored the starting files.
  The two upgrade scenarios are the exception: they correctly return to the
  state before hostupdate.
- **Not yet run as real root on a device.**

### 9.7 Notes
- **Keep hand-made records outside the DHCP pool**, which since D41 is the
  upper half of the subnet: use the lower half, or a dhcpd `fixed-address`.
  Otherwise a device that's handed that IP gets `409 conflict`.
- **IPv4 only, per D4.** No AAAA records for devices.

## 10. Dotted names, aliases and the site domain (D30–D36, 2026-10-25)

For app stacks with several names per machine, e.g. `app`, `runtask.app`,
`test.app` and `runtask.test.app`, and so the project isn't tied to one domain.

- **PocketBase.** Migration `1761500000_aliases.go`:
  - `hosts.name` becomes multi-label (max 253)
  - adds `alias_of`, a non-cascading self-relation
  - updates the help texts
  - its down migration deletes alias records and restores the one-label rule
- **Validation (`main.go`).** Each record is one of three kinds:
  - hand-made: `ipv4` set
  - device-owned: `device` set
  - alias: `alias_of` set, `ipv4` empty, target not an alias, record not
    itself a target
- **`--dns-domain`.** `serve` refuses to start without a valid one. The
  domain is used for the 253-character check and for `/api/hostupdate/info`.
  The reverse zone comes from `lanPrefix`.
- **dnssync.** It makes a second query, `$ALIAS_FILTER` (`disabled = false &&
  device = "" && ipv4 = ""`), which also catches aliases whose target was
  deleted. The Go tests check that both filters match the script.
- **Site settings.** `site.conf` → `scripts/stage` (fills `@DOMAIN@` in, and
  refuses to stage if any placeholder is left) → jobs `. site.conf`.
  - The deploy job stops if the staged Unbound or dhcpd files differ from the
    installed ones.
  - Job 22 installs what differs. Its default `APPLY=no` only reports.
  - With `DOMAIN=home.arpa` the generated files are byte-identical to the
    ones from before D31.
  - (Since D41 and D43 the installer fills in the placeholders on the box,
    and deploys every change.)
- **Devices.** The client has `--info` and `domain=` (used in messages only).
  The installer prints a `site:` step, builds its resolved, unwind and
  resolv.conf rules from the domain and reverse zone, and updates them on a
  re-run when the domain changes.
- **Tested:**
  - Go tests, using the domain `example.lan`, so nothing depends on
    `home.arpa`
  - a local PocketBase with dnssync against it: dotted names, aliases of a
    hand-made record and of a device record, a stale device darkening its
    aliases, a deleted target
  - the installer harness: 23 scenarios plus domain-change re-runs, all
    idempotent, all reverting
- **Alias takes the PTR (D36, 2026-10-26).** A device name is one label
  (`runtasktestapp`), but the machine is known by its alias
  (`runtask.test.app`). Address lookups gave the device name, so
  `socket.getfqdn()` on the machine, and host-name rules on servers it talks
  to, saw the wrong name.
  - Migration `1761600000_alias_ptr.go` adds `takes_ptr` after `alias_of` and
    `idx_hosts_takes_ptr`, `UNIQUE(alias_of) WHERE takes_ptr = TRUE AND
    alias_of != ''`. Its down migration drops both.
  - `validateAlias` gives readable errors before the index does. PocketBase
    capitalises each message's first letter, so none starts with a host name.
  - dnssync keeps each served record's PTR slot. A served alias with
    `takes_ptr` renames its target's slot; the first by name wins if the index
    is ever bypassed, and the rest are logged. With nothing ticked the output
    is byte-identical.
  - Tested: Go tests (validation, the index on its own, deleted target,
    migration revert), and 26 checks with a local PocketBase and dnssync,
    starting from the previous e2e data (upgrade path).

## 11. Rebuilding the box (D37–D39, 2026-10-26)

If the box is lost or has to be replaced, a fresh OpenBSD install becomes the
box again with one command, and with the data it had. RUNBOOK §5 has the
steps.

- **`jobs/23-install-box.sh`** does jobs 01–17's steps as one pass:
  - Each step compares before it acts (`put` checks content and
    permissions), so a re-run changes nothing that's already right.
  - `--check` only reports. On the live box it reported every file and
    service unchanged; the one exception was the PocketBase binary, which
    differed only by its embedded git commit. Builds now use
    `-buildvcs=false`, so the same source gives the same binary.
  - It stops for the person: after scheduling the address switch
    (`netswitch.sh`, which reverts unless the next run confirms it), after a
    kernel patch, and before dhcpd starts (`--start-dhcpd`: the router's DHCP
    must be off).
  - It refuses to take `ADDRESS` while something answers there. On a box
    already at `ADDRESS`, it warns if the kernel logged a duplicate address.
  - It uses the box's own LAN interface (egress, or `--interface`), whatever
    its name.
- **The bundle (D39)** lets the installer run on the box itself, with no
  workstation: `tar xzf homesubnetdns-installer.tgz`, then
  `doas sh install-box/install.sh`. It's about 10 MB and holds no secrets.
  `netswitch.sh` takes the files folder as a second argument, because the
  bundle's files aren't in the stage folder. Builds go through `scripts/build`.
- **`jobs/24-backup.sh`** stops PocketBase, copies `pb_data` except its
  `backups/`, starts it again, and archives the copy with the DHCP leases
  and an `info.txt`. **`scripts/backup`** fetches the archive into
  `~/.local/share/homesubnetdns/backups`.
- **Restore:**
  - Only onto a box with no `pb_data/data.db`; a marker holds the backup's
    sha256, so re-runs skip it.
  - The archive's entries are checked first: only `info.txt`,
    `dhcpd.leases` and `pb_data/...`, and no `..`.
  - The DHCP leases are restored only if the box has none yet.
  - dnssync gets a new random password: the `service-account` command
    creates its account, or updates the one in the restored data (§5a).
- **Tested:**
  - On the live box: `--check`.
  - As the admin user in a temporary folder on the box: archive layout and
    `check_backup` with OpenBSD's own `tar`. It accepts the real layout, and
    refuses stray entries, a missing database and `..` paths.
  - Locally: a round trip of an archive made by OpenBSD `tar` (the database
    came back byte-identical, with all records), and the dnssync password
    reset on restored data (the old password refused, the new one works).
  - The bundle: unpacked by OpenBSD `tar` as the admin user on the live box,
    then `doas sh install-box/install.sh --check`. It found its own files,
    and its report matched the remote run's (all unchanged but the binary's
    commit stamp, 11/11 checks ok). Run without root, it stops with the
    `doas` command. Its re-run messages were checked for both modes.
  - **Not run:** the install path on a fresh box, the address switch, and
    job 24 against the live PocketBase.

## 12. The network read from the box (D41–D43, 2026-11-19)

**What happened.** Within a month the LAN was renumbered twice: the router
came back from a restart on a different /24, and then a new router replaced
it, with another /24 and its own DHCP on. The box was moved to an address on
the new LAN by hand. Afterwards:
- Unbound and PocketBase still ran on the vanished address, so `rcctl check`
  said ok but neither answered on the LAN.
- dnssync failed every minute (its API URL named the old address).
- pf allowed only the subnet in between (edited by hand).
- `/etc/mygate` still named the old router, so there was no default route
  and no recursion.
- PocketBase's check-in rule would have refused every device on the new LAN.
- The repo's installer would have moved the box back to its old address.

**The design (D41).** One setting, `ADDRESS`; everything else is read where it
is true, on the box, when the installer runs:

| Value | From |
|---|---|
| Interface | the one with `ADDRESS`, else the default route's (`--interface`) |
| Netmask, prefix length | that interface's address on the same network |
| Gateway | `--gateway`, else default route, DHCP lease, `/etc/mygate`: first on the LAN |
| Subnet, network, broadcast | worked out |
| dhcpd pool | upper half of the subnet, less the box and the gateway; a /24 gives .128–.254 |
| Reverse zone | the subnet's whole octets (`2.0.192.in-addr.arpa` for 192.0.2.0/24), in PocketBase and checked by the installer |

- Templates use `@DOMAIN@ @ADDRESS@ @NETMASK@ @BITS@ @NETWORK@ @SUBNET@
  @GATEWAY@ @RANGES@`. `scripts/stage` now only copies; filling in moved to
  the box, because only the box knows the netmask and gateway. A placeholder
  the installer doesn't know stops it.
- The workstation scripts reach `BOX_USER@ADDRESS` (`scripts/box-host`)
  unless `RDOAS_HOST` is set.
- PocketBase: `setLAN` validates the flags (an IPv4 address with a /8–/30,
  not the network or broadcast address; a gateway on that network that isn't
  the box) and builds the reserved set. Its `AppURL` follows the `--http`
  address at start, so the address in the first migration is gone.
- Devices: `install.sh --box` writes `url=` to `/etc/hostupdate.conf`; the
  client has no built-in address and says to run `install.sh --box` when
  `url=` is missing. A changed `--box` rewrites the device's DNS rules.
- Job 24 no longer gives up when PocketBase can't start again (it couldn't,
  with flags naming the old address); jobs 10, 16 and 21 take the client or
  gateway from arguments or the box, not from edited constants.

**Tested.**
- `go test` covers `setLAN` on a /16 with its reserved addresses, and ten
  bad flag pairs. The other tests run on a documentation LAN (192.0.2.0/24)
  set through the same function.
- The installer's LAN step under dash and bash with stubbed `ifconfig` and
  `route`: /24, /16, /20 and /29, a router at .254, a move within the subnet,
  a fresh DHCP box, and the error cases (no address, address on another
  network, broadcast address, gateway off the LAN, no gateway).
- On the live box under OpenBSD's sh: `--check`. It derived the new subnet,
  needed `--gateway` (no default route), and showed the expected diffs for
  unbound, static.conf, pf, PocketBase, dnssync, mygate and dhcpd.conf.
- The device installer harness (fake root, stubbed tools): fresh installs
  with `--box` for resolved, unwind, dhclient, NetworkManager, dhcpcd,
  systemd and unknown setups, each re-run unchanged and uninstalled cleanly;
  an old config without `url=` (refused without `--box`, upgraded with it);
  and a box move between subnets that rewrote the resolved, unwind and
  resolv.conf rules.
- **Deployed** on 2026-11-19 with `scripts/install-box --gateway <router>`
  (no default route to read it from yet). DNS, reverse names, PocketBase and
  the check-in endpoint answered on the new address. It also installed 17 OS
  patches, one a kernel patch, which waits for a reboot. A second run, with
  no `--gateway`, said `nothing to change`; so did a run from the bundle on
  the box.
  - Those later runs didn't mention the pending reboot, which only the run
    that patched had printed. Each run now compares the version string in
    `/bsd` with the running kernel's; KARL's relink at boot reuses the
    patch's `vers.o`, so they match after a reboot. The suggested next
    command no longer carries `--check`.
  - The first run's DNSSEC check failed: dnssec-failed.org didn't answer at
    all within 15 s from this network, though isc.org validated. The check
    now needs the `ad` flag on isc.org, and fails on dnssec-failed.org only if
    it gets an answer that isn't SERVFAIL.
  - dnssync served no names until devices checked in again: every host
    record was a device last seen on the old subnet more than 24 h earlier
    (D16), and the aliases followed them.
- **dhcpd** started on 2026-11-19 (`--start-dhcpd` from the bundle, on the
  box), after the router's DHCP was turned off: a reconnecting Windows
  client's request then got no answer from the router (its lease kept the
  router's start time). dhcpd logged the request and stayed silent, since it
  never handed out that address, so clients move only when their router lease
  runs out or is released (RUNBOOK §6).

## 13. Machines on the LAN (D44, 2026-11-22)

**The ask.** A PocketBase collection showing which machines are online on
the subnet: the list an `nmap -sn` run gives, kept current. Wired and Wi-Fi
machines alike: the router puts both on the one LAN.

**The design.**
- **Probe.** Every 2 minutes (PocketBase's cron, job `lan_scan`, listed in
  the dashboard under Settings > Crons where it can be run by hand; and once
  at start), PocketBase sends a 1-byte UDP datagram to port 9 of each address
  on `--lan` but itself, 32 at a time with 100 ms between batches, to stay
  under `net.inet.ip.arpq.maxlen` (50). To deliver them the kernel ARPs for
  each address. A LAN bigger than a /20 isn't probed, only read.
- **Read.** After 3 s, `arp -an` (no root needed): the resolved entries on
  the LAN, less expired ones and ones the box publishes for others. A MAC
  with two addresses (a machine that just moved) keeps the one with the most
  time left. The box's own entry counts.
- **Names.** The reverse name from the box's own Unbound, so it's what the
  LAN sees (hosts records, aliases with `takes_ptr`, `dns` and `router` from
  static.conf); the `client-hostname` from the newest lease in
  `/var/db/dhcpd.leases` (mode 644), by start time, since dhcpd doesn't keep
  the file in order.
- **Save.** One transaction: answered machines are `online` at their address
  with `last_seen` now; others that were online go offline; machines not seen
  for 30 days are deleted. `notes` are never touched; a hostname stays after
  its lease goes. New machines and online/offline changes go to PocketBase's
  logs.
- **The ARP timeout.** OpenBSD keeps an entry 20 minutes after the last
  answer, and a probe doesn't re-ask while one is fresh, so a machine that
  left would look online for up to 20 minutes. The installer sets
  `net.inet.ip.arptimeout=300` in `/etc/sysctl.conf` and live; with the
  2-minute scan, a machine that leaves drops off within about 7 minutes.
- **Collections.** `machines` (`mac` unique, `ipv4`, `ip_order` for sorting
  by address, `online`, `dns_name`, `hostname`, `first_seen`, `last_seen`,
  `notes`) and the view `online_machines` (`online = TRUE`, by address).
  Both superuser-only, like `devices`: it's a record of the household's
  machines and when they're on, and it's in the backups.
- **`pocketbase lan-scan`** runs one scan, saves it and prints the table.
  Job 25 runs it with the flags from `/etc/rc.d/pocketbase`; the installer's
  checks run it and fail if the gateway doesn't answer.
- **Limits.** Only machines on the LAN itself: not VMs behind another
  machine's NAT, nor guest Wi-Fi if the router keeps it apart. A phone with
  a private Wi-Fi address gets a new record when that address changes, and
  one asleep may drop off. IPv4 only.

**Tested.**
- `go test`: `arp -an` parsing (the header, another interface, incomplete,
  expired and published entries, a moved machine, the box's own, one-digit
  MAC groups), `dhcpd.leases` parsing (out of order, a newest lease with no
  name, a control character, a 255-byte cap), probe targets for a /24, /20,
  /19 and /30, reverse names with a fake resolver, and saving across five
  scans (new, moved, offline, back, deleted after 30 days, an empty scan),
  the view, the MAC pattern, and the migration reverting cleanly. The older
  migration tests now count the migrations after theirs, so a new one
  doesn't break them.
- On the box, before deploying: the OpenBSD build run as the admin user, with
  a throwaway `--dir` in `/tmp`, found in 4 s every machine an earlier nmap
  run had, plus the ones that had been off then, with names and hostnames;
  `net.inet.ip.arpq.drops` stayed 0.
- The installer's `sysctl.conf` merge under dash: no file, other lines kept,
  our lines in the middle moved to the end, re-runs unchanged.
- **Deployed** on 2026-11-22 with `scripts/install-box`, after
  `scripts/backup`: `/etc/sysctl.conf` written and the ARP timeout set live
  (1200 -> 300), the new PocketBase installed (data copied to `pb_data.prev`
  first) and migrated. PocketBase's start-up scan had found every machine, so
  the installer's own scan reported `0 new`; a second `--check` said `nothing
  to change`. A capture of the box's ARP requests showed the scheduled scan
  on the even minute, a burst in one second (the rest still cached), with
  `net.inet.ip.arpq.drops` at 0.
