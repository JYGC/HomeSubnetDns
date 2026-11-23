# HomeSubnetDns: runbook

How to build, redeploy and run the home DNS and DHCP server. It's written so that
someone (or an agent) with no prior context can follow it. For the reasons
behind each design choice, see `PLAN.md`: decisions D1–D44 in §3, where the
build differs from the plan in §5a, the device check-in design in §9,
dotted names, aliases and the site domain in §10, rebuilding the box in §11,
the network read from the box in §12, and the machines-on-the-LAN scan in §13.

The box is an OpenBSD 8.0 amd64 host, `dns.<DOMAIN>`, that serves DNS and
DHCP for the LAN behind a consumer router. Nothing in the repo is tied to one
network: the box's address is `ADDRESS` in `site.conf`, and the rest is read
from the box's LAN interface (§3, D41). The examples use `home.arpa`, and the
documentation addresses of RFC 5737: the box at 192.0.2.53 on 192.0.2.0/24,
the router at 192.0.2.1. `<user>` is the box's admin user (`BOX_USER`).

## 1. What runs on the box

| Component | Role | Files on the box |
|---|---|---|
| **unbound** (OpenBSD base) | Does full DNS lookups itself with DNSSEC checking and caching, and answers names in the site domain (`site.conf` `DOMAIN`, e.g. `home.arpa`) | `/var/unbound/etc/unbound.conf`, `/var/unbound/etc/home.d/{static,pocketbase}.conf` |
| **PocketBase** v0.40.6 (our build) | Stores host records, aliases and device accounts; admin web page at `http://pocketbase.<DOMAIN>:8090/_/`. Serves `GET /api/hostupdate` for device check-ins, and `GET /api/hostupdate/info` (the domain and reverse zone, no login) for the installer (§8a). Scans the LAN every 2 minutes for the `machines` collection and the `online_machines` view (§8, D44) | `/usr/local/bin/pocketbase`, `/etc/rc.d/pocketbase`, data in `/var/pocketbase/pb_data` (user `_pocketbase`) |
| **dnssync** (Perl, base modules only) | Runs every minute from cron. Turns PocketBase `hosts` records and aliases into `pocketbase.conf`, checks it, then runs `unbound-control reload_keep_cache` | `/usr/local/libexec/dnssync`, crontab of `_dnssync` (member of `_unbound`), credentials in `/var/dnssync/.pbcreds` |
| **dhcpd** (OpenBSD base) | Hands out the upper half of the subnet except the box and the gateway (.128–.254 on a /24, less either of them if it's there), with the router as gateway, the box as DNS, and the domain `DOMAIN`. Runs only while the router's DHCP is **off** (§6) | `/etc/dhcpd.conf`, leases in `/var/db/dhcpd.leases` |
| **pf** | Stock rules, plus ports 53 and 8090 open to the LAN subnet only | `/etc/pf.conf` |
| **sysctl** | `net.inet.ip.arptimeout=300`: ARP entries last 5 minutes, not 20, so the LAN scan sees machines leave (D44) | `/etc/sysctl.conf` |

On **other devices** (OpenBSD or Linux), the optional **hostupdate** client checks
in hourly and at boot so `<name>.<DOMAIN>` follows the device's IP (§8a). Its
files there are `/usr/local/libexec/hostupdate` and `/etc/hostupdate.conf`, and
it runs as the user `_hostupdate` (OpenBSD) or `hostupdate` (Linux).

Names served from `static.conf`: `dns`, `pocketbase`, `router` (`.<DOMAIN>`).
These are reserved, so PocketBase rejects them and dnssync skips them.

## 2. Repository layout

| Path | What it is |
|---|---|
| `site.conf.example` | The site settings to copy to `site.conf` and fill in: `DOMAIN`, `ADDRESS`, `BOX_USER` (§3) |
| `unbound/`, `pf/`, `dhcpd/`, `net/` | Config file templates, which are the source of truth. The installer fills in their `@NAME@` placeholders on the box and installs them (§3) |
| `pocketbase/` | Go source for our PocketBase build: `main.go` (validation hooks, `service-account` and `lan-scan` commands), `hostupdate.go` (the device check-in endpoint), `lanscan.go` (the LAN scan, D44), their tests, `migrations/` (schema, backup cron, rate limits), `rc.d/pocketbase` |
| `dnssync/` | The sync script and its crontab |
| `hostupdate/` | The device client (`hostupdate`, POSIX sh), `install.sh` and a `README.md` how-to. They're copied to and run **on other devices**, not on the box |
| `jobs/NN-*.sh` | Scripts that run **as root on the box**, through `scripts/rdoas`. Job 23 is the installer (§5), job 24 the backup, job 25 lists the machines online; the rest are checks and captures (§8). The original setup steps (jobs 01, 04–09, 13, 14, 17, 19, 20, 22) were replaced by job 23 (D43): they installed the staged files as they are, which are now templates |
| `scripts/rdoas`, `scripts/rdoas.exp` | Copies a job to the box and runs it with `doas`, answering the password prompt with `expect` |
| `scripts/box-host` | Prints where the other scripts reach the box over SSH: `RDOAS_HOST`, else `BOX_USER@ADDRESS` |
| `scripts/stage` | Copies all deployable files (templates, the build and `site.conf`) to `~/.rdoas/stage/` on the box, under the names the installer expects. `--to DIR` writes them to a local folder instead |
| `scripts/build` | Tests PocketBase and builds it for the box as `pocketbase/pocketbase` |
| `scripts/install-box` | Sets up a box, fresh or in place of a lost one, and deploys every later change: build, stage, upload a backup, run job 23 (§3, §5) |
| `scripts/bundle` | Packs job 23, the build and the config templates into one installer to run on the box itself (§5) |
| `scripts/backup` | Copies PocketBase's data and the DHCP leases from the box to the workstation, through job 24 (§5) |
| `PLAN.md` | Design, decisions, deviations, open items, rollback |

Not in git, on purpose:
- `site.conf` and `CLAUDE.local.md`: this site's settings and notes.
- `pocketbase/pocketbase`: build output.
- `resources/`: local screenshots and notes, which may show device
  identifiers (a router's IMEI, IMSI or serial number).
- Any password file, and backups (`scripts/backup` keeps them outside the repo).

## 3. Site settings, and what comes from the network

Copy `site.conf.example` to `site.conf` and fill it in. It has the only
settings (D31, D41):

| Setting | Example | What it is |
|---|---|---|
| `DOMAIN` | `home.arpa` | The zone names are served in |
| `ADDRESS` | `192.0.2.53` | The box's LAN address. The workstation scripts reach the box there too, unless `RDOAS_HOST` is set |
| `BOX_USER` | `admin` | The user the workstation scripts log in to the box as (in group `wheel`). Empty: `~/.ssh/config` or the local user name decides. Only the workstation uses it |

The deployable files are templates. Never write the domain or anything about
the network in them: write a placeholder, and the installer fills it in on the
box, from `site.conf` and from the box's LAN interface (D41). It stops if a
placeholder it doesn't know is left.

| Placeholder | Example | Where it comes from |
|---|---|---|
| `@DOMAIN@` | `home.arpa` | `site.conf` |
| `@ADDRESS@` | `192.0.2.53` | `site.conf` |
| `@NETMASK@`, `@BITS@` | `255.255.255.0`, `24` | The netmask the LAN interface has on that network, from DHCP or a static setting |
| `@NETWORK@`, `@SUBNET@` | `192.0.2.0`, `192.0.2.0/24` | Worked out from the two above |
| `@GATEWAY@` | `192.0.2.1` | `--gateway`; else the default route, the interface's DHCP lease, `/etc/mygate`: the first that's on the LAN |
| `@RANGES@` | `.128–.254` | dhcpd's pool: the upper half of the subnet, less the box and the gateway (a box at .200 would split it into .128–.199 and .201–.254). Fixed addresses go in the lower half |

- **The LAN interface** is the one that has `ADDRESS`; else the one with the
  default route. `--interface` overrides that.
- **Sizes:** the LAN can be from a /16 to a /29.
- **PocketBase** gets the LAN from its flags in `rc.d/pocketbase`
  (`--lan=ADDRESS/BITS --gateway=…`). From them it works out which source
  addresses a check-in may come from, and the reverse zone it reports to
  devices. The installer checks that this reverse zone matches its own.

**To change a setting, or anything else in the repo:**
1. Edit it, then run `scripts/install-box --check`. That builds, stages, and
   shows each file that would change, with a diff, and every command it would
   run. It changes nothing on the box.
2. Run `scripts/install-box`. This changes DNS (and DHCP) for the whole LAN,
   so confirm first (CLAUDE.md).
   - It installs and restarts only what differs.
   - It also restarts Unbound or PocketBase if they run but don't answer on
     `ADDRESS`, e.g. after the address was changed by hand.
3. After a new `DOMAIN` or `ADDRESS`:
   - Run `install.sh` again on each device, with `--box <new address>` after
     a move (§8a).
   - Update any Windows NRPT rules by hand (§8b).

**To move the box to another address on the same LAN (D42):**
1. Set `ADDRESS` in `site.conf` to an unused address in the lower half of the
   subnet, outside both DHCP pools (the box's and, while it's on, the router's).
2. Run `RDOAS_HOST=<user>@<current address> scripts/install-box`.
   - It refuses if something already answers on the new address.
   - Otherwise it schedules the switch (`jobs/netswitch.sh` through `at(1)`)
     and stops. The SSH session to the old address drops.
3. Within 3 minutes, confirm the switch:
   ```sh
   ssh-keygen -R <new>
   ssh -o StrictHostKeyChecking=accept-new <user>@<new> true
   scripts/install-box          # no RDOAS_HOST from now on
   ```
   - The run confirms the switch once the gateway and the internet answer.
     It then moves Unbound, pf, PocketBase, dnssync and dhcpd to the new
     address.
   - Without that run, the box goes back to its previous settings, whatever
     they were: DHCP, or the old static address.
4. Run `install.sh --box <new>` on each device. With dhcpd running, DHCP
   clients get the new DNS server when they renew their lease (leases last a
   day; reconnecting a client gets it at once).

**When the LAN is renumbered** (a new router, or a router that moves its LAN
after a restart; §9):
1. Set `ADDRESS` to an address on the new network.
2. If the box still has its old address, put it on the new network first, on
   its console:
   - by DHCP, while the router's DHCP is on: `ifconfig <if> inet autoconf`
   - or with a static address: `ifconfig <if> inet <address>/<bits>`
3. Continue as for a move. If the box has no default route on the new network,
   add `--gateway <router address>`.

**Not settings, and not read from the network:**

| Value | Where |
|---|---|
| Host MAC (vmd gives a VM a new random one at each start unless its `vm.conf` sets `lladdr`) | Only used by a router's address binding list. A replacement box has a new one |
| Hostname `dns.<DOMAIN>` | Set by the installer from `DOMAIN` |
| PocketBase version | `pocketbase/go.mod` (v0.40.6, which needs Go 1.27; the toolchain downloads it automatically) |

## 4. Prerequisites

**Workstation** (Linux or WSL):
- A `site.conf` (§3).
- SSH key login to `<user>@<ADDRESS>` working with `BatchMode=yes`.
- `expect`, `dig`, and Go 1.26 or later.

**Box:**
- A fresh OpenBSD 8.0 amd64 install, with `<user>` in group `wheel`.
- The default `/etc/doas.conf` (`permit persist keepenv :wheel`).

**doas password file.** Create it outside the repo and never commit it:
```sh
umask 077; printf '%s\n' '<doas password>' > ~/.config/homesubnetdns-doas.pass
export RDOAS_PASSFILE=~/.config/homesubnetdns-doas.pass
```

**Running a job:** `scripts/rdoas jobs/NN-name.sh`. Each run starts with a
`doas (<user>@…) password:` line; that's normal, and `expect` answers it.

## 5. Set up a box: fresh, or replacing a lost one

The installer, `jobs/23-install-box.sh`, sets up a box in one pass (D37), and
is also how every later change is deployed (§3, D43). It's idempotent: each
step changes only what differs, so you run it again until it says it's done,
and a run on an up-to-date box changes nothing. `--check` only reports what
would change, so it's safe on the running box. There are two ways to run it:

- **From the workstation:** `scripts/install-box [options]` builds PocketBase,
  stages the files, uploads the backup, and runs the installer on the box as
  root over SSH.
- **On the box itself, with no workstation (D39):** `scripts/bundle` packs the
  installer, the PocketBase build and the config files (with `site.conf`
  templates) into `homesubnetdns-installer.tgz`, about 10 MB with no secrets.
  On the box: `tar xzf homesubnetdns-installer.tgz`, then
  `doas sh install-box/install.sh [options]`. Its `README.txt` has the steps.

**Keep a backup (D38).** The box's own nightly backups (§8) are lost with it.
`scripts/backup` copies PocketBase's data (host records, aliases, and the
device, admin and dnssync accounts) and the DHCP leases to the workstation, in
`~/.local/share/homesubnetdns/backups` (or `$HSD_BACKUP_DIR`), as
`homesubnetdns-<date>-<time>.tgz`. PocketBase stops for a few seconds; DNS
doesn't. It also refreshes `homesubnetdns-installer.tgz` in the same folder.
Run it after adding records or devices, and keep a copy of the newest backup
and the bundle off the workstation too (a USB stick, a cloud drive), so a
rebuild doesn't depend on it. Without a backup the new box starts empty
(step 6).

⚠ marks the steps where a person has to act.

1. **The old box.** If it still runs, run `scripts/backup`, then shut it down
   (`doas shutdown -p now` on it): two machines must never hold
   `ADDRESS`. If it's dead, use the newest backup file.
   - While no box runs, the LAN has no DHCP and no `<DOMAIN>` names. Devices
     keep their addresses for up to a day (the lease time); new ones get none.
     If that's too long, turn the router's DHCP on meanwhile (§6), and off
     again before step 5.
2. ⚠ **Install OpenBSD 8.0 amd64** on the new machine:
   - hostname `dns`, DNS domain `DOMAIN` from `site.conf`
   - the network: DHCP, while the router's DHCP is on; or a static address
     on the LAN with its netmask, default route and DNS server. The installer
     reads the netmask and gateway from this, and moves the box to `ADDRESS`.
   - a user in group `wheel` (`BOX_USER`); sshd on
   - Afterwards, as root: if `/etc/doas.conf` doesn't exist, run
     `echo 'permit persist keepenv :wheel' > /etc/doas.conf`.
3. **The workstation** (§4). Needed for `scripts/install-box`, and for
   day-to-day work on the box afterwards:
   - Forget the old box's keys: `ssh-keygen -R <ADDRESS>; ssh-keygen -R dns.<DOMAIN>`.
   - `ssh-copy-id <user>@<address>`
   - Put the new box's doas password in the `$RDOAS_PASSFILE` file.
4. **Run the installer**, with `--restore <backup file>`, or without it for
   an empty start:
   - **From the workstation:** `scripts/install-box --restore <backup file>`.
     If the box isn't at `ADDRESS` yet, put `RDOAS_HOST=<user>@<address>`
     in front. If it says it can't tell the gateway, add `--gateway <router address>`.
   - **On the box:** copy `homesubnetdns-installer.tgz` and the backup there,
     by `scp` or on a USB stick (`dmesg` shows its disk, e.g. `sd1`; then
     `doas mount_msdos /dev/sd1i /mnt`). Then, as that user, in a folder
     whose path has no spaces: `tar xzf homesubnetdns-installer.tgz`, then
     `doas sh install-box/install.sh --restore <backup file>`.

   It stops and prints the exact command to run next:
   - **Address switch:** it moves the box to `ADDRESS` in the background.
     Within 3 minutes, run it again; that run confirms the switch, otherwise
     the box goes back to its old settings. An SSH session to the old address
     drops: reconnect after `ssh-keygen -R <ADDRESS>` and
     `ssh -o StrictHostKeyChecking=accept-new <user>@<ADDRESS> true`
     (with `scripts/install-box`, leave out `RDOAS_HOST` from then on).
   - It ends with dhcpd installed but not started.
   - **Kernel patch:** OS patches come last. While a patched kernel waits
     for a reboot (`/bsd`'s version string differs from the running
     kernel's), every run says so: reboot (`doas shutdown -r now`, or from
     the workstation `timeout 60 scripts/rdoas jobs/03-reboot.sh`), then run
     it again.
5. ⚠ **DHCP. Never run two DHCP servers at once.** Make sure the router's DHCP
   server is off (§6) and the old box is down. Then run it again with
   `--start-dhcpd` added. It should end with
   `done: this box serves DNS and DHCP for the LAN`.
6. **With a backup**, devices carry on at their next hourly check-in.
   ⚠ **Without one:**
   - Create the PocketBase admin account within 30 minutes, from the link
     `grep -o "http://[^ ]*pbinstall/[^ ]*" /var/log/daemon | tail -1` prints
     on the box (from the workstation, run it through `ssh <user>@<ADDRESS>`). The person does this themselves; never put admin credentials in
     chat or files. If the link has expired, restart PocketBase
     (`rcctl restart pocketbase` through a job) and get the new one.
   - Add the hand-made hosts records again.
   - Re-run `install.sh --box <ADDRESS>` on each device: it prints the name
     and password to enter under **devices** (§8a).
7. **Check:** the installer's own checks, then §7 from the workstation.
   Optionally, run job 11 (resilience), then a reboot test:
   `timeout 60 scripts/rdoas jobs/03-reboot.sh`, wait for SSH, then job 12.
8. **The router's binding list** (§6) is only used by the router's DHCP. If
   you ever turn that back on, change the box's MAC there.

What the installer does, in order:

| Step | What it does |
|---|---|
| The LAN | Finds the LAN interface, netmask and gateway, works out the subnet, DHCP pool and reverse zone, and fills in the templates (§3) |
| Originals | `*.orig` copies of the files it replaces, unless they're ours already |
| Network | `ADDRESS` static on the LAN interface, and the default route. Already there: it fixes `/etc/hostname.<if>`, `/etc/mygate` and the live default route. Not there: it refuses if something answers on `ADDRESS`, else switches; the switch reverts unless the next run confirms it (D42) |
| ARP | `net.inet.ip.arptimeout=300` in `/etc/sysctl.conf` and live (D44) |
| Users | `_pocketbase` and `_dnssync` |
| Unbound | Config, trust anchor, start; restart if the config changed or it doesn't answer on `ADDRESS` |
| pf | Rules; resolvd off; `resolv.conf` → 127.0.0.1 |
| PocketBase | Data from `--restore` (only while there's none), binary (with a copy of `pb_data` first), rc.d, dnssync's login (new random password, also after a restore), start; restart if anything changed or it doesn't answer on `ADDRESS` |
| dnssync | Script, crontab |
| Hostname | `dns.<DOMAIN>` |
| dhcpd | Config; enabled and started only with `--start-dhcpd`; restarted when its config changes |
| OS patches | `syspatch`; says so if the kernel needs a reboot |
| Checks | Services, the default route, a lookup, DNSSEC, `dns.<DOMAIN>` and its reverse name, the PocketBase endpoints (the domain and reverse zone devices get), a dnssync run, a LAN scan that the gateway must answer |

`--interface IF` names the LAN interface if it can't tell, and `--gateway IP`
the router. A bundle is built from the repo as it is, uncommitted changes
included; its `README.txt` names the commit.

## 6. The router

The box needs little from the router: it's the LAN's gateway, and its DHCP
server must be off while the box's dhcpd runs. Consumer routers often can't
hand out a DNS server of your choice (D8), which is why the box runs DHCP
itself.

- **Login:** the person logs in to its admin page; never put the router's
  credentials in chat or files.
- **To hand DHCP to the box (again):**
  1. Check that the router's DHCP pool doesn't include `ADDRESS`, and that
     nothing else uses it.
  2. Turn off the router's DHCP server, usually on its LAN or DHCP settings
     page.
  3. Straight away, run `scripts/install-box --start-dhcpd`.
  4. Clients move to the box only when their router lease runs out, up to a
     day later. Reconnecting doesn't do it: the client asks to keep its
     address, the router no longer answers, dhcpd stays silent about an
     address it never handed out, and Windows keeps an unexpired lease while
     the gateway answers. Until a client moves it uses the router's DNS, so
     `<DOMAIN>` names don't resolve there. To move one now, make it give its
     address up first:
     - Windows (admin): `ipconfig /release "Wi-Fi"; ipconfig /renew "Wi-Fi"`
     - phones: forget the Wi-Fi network and join again
     - Linux: `nmcli connection down <conn> && nmcli connection up <conn>`,
       or reboot

  Never run both DHCP servers. To go back: `rcctl disable dhcpd` and
  `rcctl stop dhcpd` on the box first (through a job), then turn the router's
  DHCP on.
- **IPv6:** if the router announces an IPv6 DNS server (RDNSS in its router
  advertisements, or DHCPv6), devices can ask it and bypass the box (D4).
  Router advertisements with no prefix and no DNS server are harmless. Check
  with `jobs/21-diagnose-clients.sh`, again whenever the router or the ISP
  changes.

## 7. Verification checklist

From the workstation, with `S=<ADDRESS>`, e.g. `S=192.0.2.53`:

| Check | Command | Expect |
|---|---|---|
| Lookup | `dig @$S openbsd.org +short` | an address; a second query is fast |
| DNSSEC | `dig @$S dnssec-failed.org` | `SERVFAIL` |
| DNSSEC, signed domain | `dig @$S isc.org +dnssec` | `ad` flag set |
| Local name | `dig @$S dns.home.arpa +short` | `$S` |
| Reverse lookup | `dig @$S -x $S +short` | `dns.home.arpa.` |
| Unknown local name | `dig @$S nope.home.arpa` | `NXDOMAIN` |
| TCP | `dig @$S openbsd.org +tcp +short` | an address |
| PocketBase → DNS | Add a record in the admin page, then `dig @$S <name>.home.arpa` within 60 s | its IP |
| Services | `jobs/12-post-reboot.sh` | unbound, pocketbase, cron, dhcpd `(ok)` (dhcpd only once it has taken over from the router, §6); resolvd `(failed)` is intended |
| The installer | `scripts/install-box --check` | `nothing to change`, and every check `ok` |
| Sync account | `jobs/11-phase8-resilience.sh` | creating a record as the service account gives HTTP 403; with PocketBase stopped, DNS still works |
| Check-in endpoint | `curl -s -w ' %{http_code}\n' -u nobody:x http://$S:8090/api/hostupdate` | `badauth 401` |
| Machines online | Admin page, collection `online_machines`; or `scripts/rdoas jobs/25-lan-scan.sh` | every machine that's on, wired or Wi-Fi; the same as `nmap -sn <subnet>` from the workstation |
| Device check-in | On a device: `su -s /bin/sh hostupdate -c /usr/local/libexec/hostupdate` (`_hostupdate` on OpenBSD), then `dig @$S <name>.home.arpa +short` within 60 s | `<name>.home.arpa -> <ip>`, then that IP |

## 8. Day-2 operations

All changes are made in the repo first, then deployed with the installer:
`scripts/install-box --check` to see what would change, then
`scripts/install-box` (§3). It builds and stages first.

- **Add, change or remove a host:** use the PocketBase admin page, collection
  `hosts`. Fields:
  - `name`: served as `<name>.<DOMAIN>`. One label (`nas`) or several
    (`runtask.test.app`, D30). Names under `dns`, `pocketbase` or `router` are
    reserved
  - `ipv4`: empty on aliases
  - `alias_of`: makes the record an alias (below)
  - `takes_ptr`: on an alias, makes the address lookup give this name (below)
  - `no_ptr`: tick to skip the reverse record
  - `disabled`
  - `notes`

  Changes reach DNS within 60 s; they're served with a 60-second lifetime.
  Devices that have a record need a **fixed IP**: either set it on the device,
  in the lower half of the subnet, or add a `host` entry in
  `dhcpd/dhcpd.conf` and run the installer (it restarts dhcpd).
- **See which machines are on the LAN (D44):** the admin page, collection
  `online_machines`, lists the machines that answered the latest scan (every
  2 minutes), by address: `ipv4`, `dns_name` (the box's reverse name),
  `hostname` (from its DHCP request to the box), `mac`, `first_seen`,
  `last_seen`. `machines` has every machine seen in the last 30 days, with
  `online` ticked for those on now; write what a machine is in its `notes`.
  To scan at once: Settings > Crons > `lan_scan`, or
  `scripts/rdoas jobs/25-lan-scan.sh`, which prints the list. New machines
  and machines going on and off are in the admin page's Logs.
- **Give a machine more names (aliases, D33–D36):**
  - Add a `hosts` record with the new `name`, empty `ipv4`, and `alias_of`
    set to the machine's record (hand-made, or a device's). It's served
    with that record's current address, follows a device's check-ins, and
    drops out while its target isn't served. It has no PTR of its own.
  - To make the address lookup (PTR) give the alias's name instead of the
    target's, tick `takes_ptr` on that alias (D36): one alias per target.
    The target gets its own name back while the alias is disabled or not
    served. This matters for programs that turn an address back into a name,
    e.g. Python's `socket.getfqdn()` on the machine, or host-name rules in
    `pg_hba.conf` or MySQL grants on servers it connects to.
  - The target must have an address itself; aliases don't chain.
  - Deleting the target leaves the alias with an empty `alias_of`, not
    served (dnssync logs `alias target was deleted`). Point it elsewhere.
  - A device can't take an alias's name: its check-ins get `conflict`.
  - Several names on one machine: the machine tells them apart by port, or
    by a reverse proxy that routes on the host name.
- **Change the fixed names:** edit `unbound/static.conf` **and**
  `reservedNames` in `pocketbase/main.go`, then run the installer.
- **Change dnssync, or PocketBase (`main.go`, `hostupdate.go`, migrations,
  `rc.d`):** run the installer. `scripts/build` runs the tests and builds
  with `-buildvcs=false`, so the same source gives the same binary and the
  installer only restarts PocketBase for real changes. For a new binary it
  copies `pb_data` to `/var/pocketbase/pb_data.prev` first; PocketBase applies
  new migrations when it starts.
- **Change site settings (`site.conf`), or move the box:** §3.
- **Upgrade PocketBase:**
  - In `pocketbase/`, run `go get github.com/pocketbase/pocketbase@vX.Y.Z && go mod tidy`.
  - Read the PocketBase changelog for breaking changes.
  - Run the installer.
- **OS patches:** the installer applies them (job 02 does only that). If it
  asks for a reboot, run job 03, then job 12.
- **Logs and data:**
  - `/var/log/daemon`: unbound, pocketbase, dnssync and dhcpd
  - `/var/cron/log`: cron runs
  - `/var/db/dhcpd.leases`: DHCP leases
  - `/var/pocketbase/pb_data/backups`: nightly PocketBase backups (03:00, 7 kept, on the box only)
- **Back up the data off the box:** `scripts/backup` (§5, D38). Run it after
  adding records or devices; a lost box can only be restored from these.
- **Debugging:** run `scripts/rdoas jobs/18-show-records.sh` to see the generated
  records. Jobs 10 (DNS to and from the router), 15 (DHCP) and 16 (one
  client's DNS: `scripts/rdoas jobs/16-phone-dns.sh <client address>`) are
  tcpdump captures.
- **A client can't resolve `<DOMAIN>` names, but `dig @<ADDRESS> <name>`
  works:**
  - Run `scripts/rdoas jobs/21-diagnose-clients.sh <client address>` while
    the client looks a name up.
  - The job shows service health, the router's IPv6 announcement, and whether
    the client's queries reach the box. If none do, the client is using the
    router's IPv6 DNS (D4).
  - On Windows, `Get-DnsClientServerAddress` shows the router (or an
    `fe80::…` server) on the adapter, and `nslookup` reports the router as
    its server.

## 8a. Device check-ins (hostupdate)

A device with an account in the PocketBase `devices` collection runs
`hostupdate` hourly (at a random minute) and at boot. That's
`GET /api/hostupdate` with Basic auth. The server sets that device's `hosts`
record to the **request's source IP**, and dnssync serves it within 60 s. The
rules, which the Go tests check:

| Situation | Result | Reply |
|---|---|---|
| First check-in | A new `hosts` record is created, or a hand-made one with the same name is adopted (its notes, `no_ptr` and `disabled` are kept) | `good <ip>` |
| Same IP as last time | Only `last_seen` is updated | `nochg <ip>` |
| Wrong name or password, or the device is `disabled` | Nothing changes | `badauth` (401) |
| Source outside the LAN subnet, or its network, gateway, box or broadcast address | Nothing changes | `badip <ip>` (400) |
| A hand-made record with a PTR holds that IP | Refused; hand-made records are never changed | `conflict <name>` (409) |
| An alias has the device's name | Refused; nothing changes (D35) | `conflict <name>` (409) |
| Another *device's* record holds that IP | That record's `ipv4` is cleared, and this device gets the IP | `good <ip>` |
| No check-in for 24 h | Not served (dnssync filter), until the next check-in | – |
| The device account is deleted | Its `hosts` record is deleted too (cascade) | – |

**Add a device** (the person does the steps on the device; `hostupdate/README.md`
has the full device-side how-to, including troubleshooting):
1. Copy the `hostupdate/` directory to the device, e.g. `scp -r hostupdate <device>:`.
2. On the device, as root: `sh hostupdate/install.sh --box <ADDRESS> [NAME]`.
   NAME defaults to the short hostname. `--box` is needed the first time and
   after the box moves; it's saved as `url=` in `/etc/hostupdate.conf`. The
   installer:
   - creates the run-as user and installs the client
   - generates a password into `/etc/hostupdate.conf` (mode 0400)
   - prints the name and password
3. In the admin page, collection **devices**, add a record with that `name` and
   password (`email` can stay empty). Press Enter in the installer to test.
4. The installer also:
   - adds the crontab (`crontab -u <user> -l`), or, on Linux without cron,
     `hostupdate.timer`
   - asks the box for its domain and reverse zone (`hostupdate --info`, D32)
     and saves them in `/etc/hostupdate.conf`; `--domain` is the fallback if
     the box doesn't answer
   - unless `--no-dns` is given, sets up the device's DNS so names in that
     domain resolve there (§8b; the method table is in
     `hostupdate/README.md` "DNS")

   Re-running it on an installed device is safe. It keeps the name, password,
   box address and check-in minute, and changes only what differs.

**After the box moves, or the LAN is renumbered:** run
`sh hostupdate/install.sh --box <new ADDRESS>` on each device. Devices
installed with the first client, which had the box's address built in, have
no `url=` saved, so they need `--box` once anyway.

**Remove a device:** on the device, `sh hostupdate/install.sh --uninstall`. Then
delete its `devices` record, which also deletes its DNS record. To suspend a
device instead, tick `disabled` on its devices record (check-ins get
`badauth`) or on its hosts record (check-ins succeed but it isn't served).

**Client notes:**
- **HTTP tools:** it uses the first one available of curl, GNU wget, OpenBSD
  `ftp(1)` and BusyBox wget. curl and GNU wget keep the password out of `ps`.
  The other two put it in the URL for the length of the request, which D19
  accepted. Set `HOSTUPDATE_CLIENT=curl|gnuwget|ftp|bbwget|"busybox wget"` to
  force one when debugging.
- **Messages and logs:** it prints messages when run from a terminal. Otherwise
  it logs to syslog with the tag `hostupdate`: on OpenBSD in
  `/var/log/messages`, on Linux in `journalctl -t hostupdate`. A `nochg` result
  is silent.
- **Devices with more than one LAN address** get whichever address they use to
  reach the box: a machine with a NIC and a virtual interface on the LAN may
  be served as the virtual one. If that matters for a device, change its
  routing, or add a `source=` option to the client (curl `--interface`, wget
  `--bind-address`, `ftp -s`). The server would still only ever record the
  real source address.
- **Hand-made records** should use IPs outside the DHCP pool, i.e. in the
  lower half of the subnet (.2–.127 on a /24), or have a dhcpd `host` entry.
  Otherwise a device that's given that IP gets `conflict`.

## 8b. Sending `<DOMAIN>` lookups to the box on each device

`hostupdate/install.sh` applies these automatically (D26–D28; see
`hostupdate/README.md` "DNS"). The manual recipes below are for devices the
installer skips, for Windows, or for devices that don't run hostupdate. Use
them when a device keeps using the router's IPv6 DNS (§6, D4). Each recipe
sends `home.arpa` and reverse 192.0.2.x lookups to 192.0.2.53, flushes the
cache, then checks the result through the system resolver, as apps would.
`dig` and `nslookup` can bypass these rules, so check with the commands shown.
Put in your `site.conf` `DOMAIN` and `ADDRESS` for `home.arpa` and
`192.0.2.53`, and for `2.0.192.in-addr.arpa` the reverse zone that
`/api/hostupdate/info` reports; `nas` is any name the box serves. After the
box moves, redo these by hand: nothing updates them.

**Windows** (admin PowerShell. The first line removes a rule for an earlier
address of the box):
```powershell
Get-DnsClientNrptRule | Where-Object Namespace -Contains ".home.arpa" | Remove-DnsClientNrptRule -Force
Add-DnsClientNrptRule -Namespace ".home.arpa", ".2.0.192.in-addr.arpa" -NameServers "192.0.2.53"
Clear-DnsClientCache
Resolve-DnsName nas.home.arpa
```
Undo: `Get-DnsClientNrptRule | Remove-DnsClientNrptRule -Force`.

**Linux with systemd-resolved** (Ubuntu, Fedora, Arch; also any system where
`/etc/resolv.conf` points to `127.0.0.53`). Lookups for these domains go to
the box only:
```sh
sudo mkdir -p /etc/systemd/resolved.conf.d
printf '[Resolve]\nDNS=192.0.2.53\nDomains=~home.arpa ~2.0.192.in-addr.arpa\n' |
    sudo tee /etc/systemd/resolved.conf.d/home-arpa.conf
sudo systemctl restart systemd-resolved
sudo resolvectl flush-caches
resolvectl query nas.home.arpa      # or: getent hosts nas.home.arpa
```
Undo: delete the file, then restart `systemd-resolved`.

**Linux with NetworkManager** (with or without resolved). Drop the router's
IPv6 DNS for that connection entirely; IPv4 DHCP gives the box, once dhcpd runs:
```sh
nmcli -g NAME connection show --active              # find the connection name
sudo nmcli connection modify "<name>" ipv6.ignore-auto-dns yes
sudo nmcli connection up "<name>"
getent hosts nas.home.arpa
```
With systemd-networkd instead, set `UseDNS=no` under `[IPv6AcceptRA]` and
`[DHCPv6]` in the `.network` file.

**Linux with dhcpcd** (Slackware `rc.inet1`, Raspberry Pi OS, Arch). Stop
dhcpcd from taking the router's IPv6 DNS: DNS then comes only from DHCP, which
is the box at home and the local server elsewhere.
```sh
printf 'nooption nd_rdnss\nnooption dhcp6_name_servers\n' | cat - /etc/dhcpcd.conf > /tmp/dhcpcd.conf &&
    sudo cp /tmp/dhcpcd.conf /etc/dhcpcd.conf    # at the top: before any "interface" line
dhcpcd -V | grep -E 'rdnss|name_servers'        # the option names exist
sudo /etc/rc.d/rc.inet1 restart                 # Slackware; elsewhere restart dhcpcd or reboot
getent hosts nas.home.arpa
```

**Linux with a plain `/etc/resolv.conf`** (dhclient, ifupdown, Alpine), if it lists the router's
`fe80::` server:
- glibc has no per-domain routing. It asks the nameservers in order, and the
  first answer wins, even a "no such name".
- So make sure `nameserver 192.0.2.53` is first, using whatever writes the
  file. With openresolv, put `name_servers=192.0.2.53` in
  `/etc/resolvconf.conf`.
- musl (Alpine) asks all nameservers at once, so there the router's entry has
  to be removed.
- There's no cache to flush unless `nscd` runs (`sudo nscd -i hosts`).
- Check with `getent hosts nas.home.arpa`.

**OpenBSD with unwind(8)** (base). This is the per-domain equivalent: resolvd
points `/etc/resolv.conf` at unwind on 127.0.0.1 automatically.
```sh
printf 'forwarder { 192.0.2.53 }\nforce forwarder { home.arpa 2.0.192.in-addr.arpa }\n' |
    doas tee /etc/unwind.conf
doas rcctl enable unwind
doas rcctl restart unwind               # also how to flush its cache
unwindctl status
getent hosts nas.home.arpa
```
- If unwind rejects the answers as DNSSEC failures, use
  `force accept bogus forwarder { … }`.
- Simpler alternative, as on the box itself (D7):
  - `doas rcctl disable resolvd && doas rcctl stop resolvd`
  - then make `/etc/resolv.conf` read `nameserver 192.0.2.53` and `search home.arpa`.
- libc has no cache of its own.

## 9. Gotchas

- **Background processes die when the `ssh -tt` session closes.** A
  `nohup … &` reboot never happened. Run things in the foreground, or queue them
  with `at`.
- **`doas -u <user>` asks root for a password.** Inside jobs, use
  `su -s /bin/sh <user> -c '…'` instead.
- **`rcctl set <svc> flags` fails until the service is enabled.** Run `rcctl enable` first.
- **dhcpd needs `/var/db/dhcpd.leases` to exist.** The installer creates it.
- **dhcpd checks an address with ping before offering it.** An address still
  held by a device with an old router lease is marked "abandoned" for 24 h and
  skipped.
- **dhcpd drops leases outside its subnet when it starts.** After the LAN is
  renumbered it empties `/var/db/dhcpd.leases`; the old leases are only in
  the backups.
- **The stock OpenBSD `pf.conf` allows all incoming traffic.** That's why there
  are explicit `block` rules for ports 53 and 8090.
- **Unbound includes `home.d/*.conf`.** dnssync's temp and backup files are
  dotfiles that don't end in `.conf`, so they are never loaded by mistake.
- **PocketBase bool fields can't default to true.** That's why the fields are
  `no_ptr` and `disabled` (false means on).
- **PocketBase only runs app migrations in `serve`.** The custom
  `service-account` command runs them itself before creating the account.
- **Windows (and WSL through it) keeps the old DNS** until the network adapter
  reconnects, or with `ipconfig /release` + `/renew` as admin.
- **OpenBSD `ftp(1)` prints the whole URL in its errors, including any
  `user:password@`.** hostupdate never passes a tool's stderr through; it only
  takes the HTTP status code from it.
- **GNU wget prints no response body on a 401, even with `--content-on-error`.**
  It exits with status 6, which hostupdate maps to `badauth`. It does print the
  body for other error codes.
- **OpenBSD's `head` has no `-c`.** Use `fold -w N | head -n 1` to cut random strings.
- **WSL checks in through NAT.** A test from WSL arrives from the Windows PC's
  LAN IP. With WSL's mirrored networking off, WSL itself is on 172.x behind
  Windows, and still reaches the LAN.
- **cronie won't run a job for a user whose home is missing (D40).** It logs
  `ERROR chdir failed` and stops, while Debian's cron carries on. `su` only warns,
  so a check-in by hand or from the installer works while every scheduled one
  fails, and the device drops out of DNS 24 h later (D16). cron also keeps the
  home it saw when it read the crontab, so a new home does nothing until the
  crontab is written again; the installer does that. To test the schedule, look
  for a real cron run in the log, not a run by hand.
- **Routers can renumber the LAN.** A router may come back from a restart on
  another subnet, and a new router usually brings its own. A box with a
  static address is then cut off, and everything that named the old network
  breaks. Since D41 only `ADDRESS` names it; §3 has the steps.
- **A service bound to an address that has gone keeps running, and answers
  nothing.** After the box's address is changed by hand, Unbound and
  PocketBase still run (so `rcctl check` says `ok`) on the old address, and
  dnssync fails every minute with `599 Internal Exception`. The installer
  asks each one on `ADDRESS` and restarts it if it doesn't answer.
- **PocketBase can't start with `--http` on an address the box doesn't
  have.** Job 24 once stopped it for a backup and couldn't start it again; the
  backup now carries on regardless and says so.
- **Never use a `<DOMAIN>` name in a device's `pf.conf`, least of all on the
  VM host the box runs on.** pf looks names up once, when it loads, and at
  boot the box isn't up yet. A line like `rdr-to dns.home.arpa port domain`
  then fails, so pf keeps `/etc/rc`'s boot-time ruleset (block all but SSH
  in, DNS, ping and DHCP out). Its symptom: `connect: Permission denied` on
  anything else, e.g. hostupdate's port 8090. Name the box's address instead,
  and check with `pfctl -nf /etc/pf.conf`.
- **The machine list lags behind a machine that leaves** by up to about 7
  minutes: the box keeps its ARP entry 5 minutes after the last answer
  (D44), and the scan runs every 2. A machine that arrives shows within 2.
- **The machine list shows only machines on the LAN itself.** VMs behind
  another machine's NAT reach the LAN through that machine's address, so
  only it shows. Neither do devices on a guest Wi-Fi network, or on a Wi-Fi
  network with client isolation, if the router keeps them apart.
- **Phones with private Wi-Fi addresses** get a new `machines` record each
  time their MAC changes; the old one drops out after 30 days. A sleeping
  phone may stop answering, and show as offline while it's in a pocket.
- **Files edited by hand on the box are put back by the installer**, e.g. pf
  rules edited to allow only an old subnet. Its `--check` shows the diff
  first.

## 10. Rollback

See `PLAN.md` §7. In short:
1. `rcctl disable dhcpd` and `rcctl stop dhcpd`, **then** turn the router's DHCP back on.
2. Restore the `*.orig` files.
3. Disable and stop `pocketbase` and `unbound`, and remove `_dnssync`'s crontab.
4. Re-enable `resolvd`, then run `sh /etc/netstart <LAN interface>` and
   `pfctl -f /etc/pf.conf`.
5. Remove the two HomeSubnetDns lines from `/etc/sysctl.conf`, then
   `sysctl net.inet.ip.arptimeout=1200`.

## 11. Secrets

| Secret | Where it lives |
|---|---|
| doas password | Only in the file `$RDOAS_PASSFILE` points to, outside the repo |
| dnssync service account | Generated on the box (again after a restore); `/var/dnssync/.pbcreds`, mode 0600, `_dnssync` |
| Backups (`scripts/backup`) | On the workstation, `~/.local/share/homesubnetdns/backups`, mode 0600. They hold every record, the machines seen on the LAN, and the bcrypt hashes of the admin, device and dnssync passwords: never in the repo or in chat. The installer bundle next to them holds no secrets |
| Device passwords | Generated by `install.sh` on each device; `/etc/hostupdate.conf` there (mode 0400, run-as user), and as a bcrypt hash in the PocketBase `devices` record |
| PocketBase admin login | Held by the owner; created through the setup link |
| Router admin password | Held by the owner |
