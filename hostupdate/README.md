# hostupdate

Keeps `<name>.<domain>` pointing at this device. Once an hour, and once at
boot, the device checks in with PocketBase on the DNS box (`site.conf`
`ADDRESS` in the HomeSubnetDns repo).
PocketBase records the address the check-in came from, and DNS serves it within
a minute. A device that stops checking in drops out of DNS after 24 hours.

The domain is the box's `site.conf` `DOMAIN`, e.g. `home.arpa`, which the
examples below use, with `<box>` for the box's address. The installer asks the
box for the domain and the reverse zone, so nothing on the device is tied to
one domain or network. You give it the box's address once (`--box`); it's
saved in the config.

It works on OpenBSD and Linux. It needs `/bin/sh` and one of curl, GNU wget,
OpenBSD's `ftp(1)` (in base) or BusyBox wget. It runs as its own unprivileged
user, not root.

The installer also sets up the device's DNS so that names in the domain resolve
there (see [DNS](#dns)). It's safe to run again on a device that already has
hostupdate: it changes only what differs.


The server side and the rules it applies are in `RUNBOOK.md` §8a in the
HomeSubnetDns repo.

## Install

1. **Copy this folder to the device.** The `:` at the end is required;
   without it, scp makes a local copy instead:
   ```sh
   scp -r hostupdate <user>@<device-ip>:
   ```
2. **Run the installer on the device, as root:**
   ```sh
   sudo sh hostupdate/install.sh --box <box>    # Linux
   doas sh hostupdate/install.sh --box <box>    # OpenBSD
   ```
   - `--box` is needed the first time, and after the box moves. Re-runs
     otherwise use the address saved in `/etc/hostupdate.conf`.
   - The name defaults to the device's short hostname. To use another one, add
     it as an argument: `sh hostupdate/install.sh nas`.
   - Add `--no-dns` to leave the device's DNS settings alone.
   - Add `--domain <domain>` only if the box can't be reached during the
     install. Normally the installer gets the domain from the box.
   - Names are lowercase letters, digits and inner hyphens, up to 63 characters.
   - `dns`, `pocketbase` and `router` are reserved.
3. **Create the device in PocketBase.** The installer prints a name and a
   generated password. It only does this while check-ins fail, so a re-run on
   a working device skips this step.
   - Open http://pocketbase.home.arpa:8090/_/ and go to **devices** →
     **New record**.
   - Enter the name, and paste the password into both password fields. Email
     can stay empty. Save.
   - If a hand-made `hosts` record with that name already exists, the first
     check-in takes it over and keeps its notes and settings. If an **alias**
     has that name, check-ins fail with `conflict` until one is renamed.
4. **Test.** Press Enter in the installer. Expect:
   ```
   hostupdate: <name>.home.arpa -> <this device's address>
   check-in: OK; <name>.home.arpa resolves within a minute
   ```
   Before the prompt, the installer prints one line per step: `user:`,
   `client:`, `box:`, `site:` (the domain, and where it came from), `config:`,
   `schedule:`, `dns:`. Each says what it did, or `unchanged`.
5. **Check DNS** from any machine on the LAN:
   ```sh
   dig @<box> <name>.home.arpa +short
   ```

## What gets installed

| | OpenBSD | Linux |
|---|---|---|
| Run-as user | `_hostupdate` | `hostupdate` |
| Client | `/usr/local/libexec/hostupdate` | same |
| Config (mode 0400, owned by the run-as user) | `/etc/hostupdate.conf` | same |
| Schedule | that user's crontab: hourly at a random minute, plus `@reboot … --boot` | the same crontab; with no cron, `hostupdate.timer` and `hostupdate.service` in `/etc/systemd/system` |
| DNS | `/etc/unwind.conf` lines (see [DNS](#dns)) | one of the methods in [DNS](#dns) |
| What the installer changed in DNS (for `--uninstall`) | `/etc/hostupdate.state` | same |
| Logs (syslog tag `hostupdate`) | `/var/log/messages` | `journalctl -t hostupdate` (or `/var/log/syslog`) |

The config file holds:
```
name=<name>
password=<32 letters and digits>
domain=home.arpa                          # from the box; messages and re-runs
reverse=2.0.192.in-addr.arpa              # from the box; re-runs
url=http://<box>:8090/api/hostupdate      # from --box
```

## Day to day

- **Check in now:**
  ```sh
  sudo -u hostupdate /usr/local/libexec/hostupdate                 # Linux
  doas su -s /bin/sh _hostupdate -c /usr/local/libexec/hostupdate  # OpenBSD
  ```
  It prints the result when run from a terminal; an unchanged address says
  `(unchanged)`. From cron it's silent unless something changed or failed.
- **See the schedule:** `crontab -u hostupdate -l` (`_hostupdate` on OpenBSD),
  or `systemctl list-timers hostupdate.timer`.
- **Update the client or the DNS setup:** copy the new folder over and run
  `install.sh` again. Do the same after the box's domain changes: the re-run
  picks up the new one and rewrites the DNS rules.
  - It keeps the name, the password, the box's address and the check-in
    minute, so PocketBase needs no change.
- **The box moved** (a new `ADDRESS`, or the LAN was renumbered): run
  `install.sh --box <new address>`. It saves the new address, gets the new
  reverse zone, and points the DNS rules at the box again. Until then,
  check-ins fail with `cannot reach`, and the device drops out of DNS after
  24 hours.
  - If the device's network setup has changed (e.g. it moved to
    NetworkManager), it undoes the old DNS change and applies the right one.
- **More names for this device:** in PocketBase **hosts**, add a record per
  extra name, e.g. `api.app` or `runtask.test.app`. Leave `ipv4` empty, and
  set `alias_of` to this device's record.
  - The aliases follow the device's address.
  - If the machine is known by one of them, tick `takes_ptr` on that one, so
    looking up the address gives that name, not the device name. That's
    what `socket.getfqdn()` and host-name rules (e.g. in `pg_hba.conf`) see.
  - They drop out of DNS with it after 24 hours without a check-in.
  - The device keeps its one account name. The device itself has to tell the
    names apart, by port or with a reverse proxy.
- **Rename the device:**
  1. Run `install.sh <newname>`; it keeps the password.
  2. Rename the **devices** record in PocketBase to match.
  3. The next check-in renames the DNS record.
- **New password:** delete `/etc/hostupdate.conf`, run `install.sh` again, and
  paste the new password into the **devices** record.
- **Pause:**
  - Tick `disabled` on the **devices** record. Check-ins are then refused with
    `badauth`.
  - Or tick `disabled` on its **hosts** record. Check-ins still succeed, but
    the name isn't served.
- **Remove:**
  1. Run `sudo sh hostupdate/install.sh --uninstall`. This removes the
     schedule, client, config and user, and undoes its DNS changes.
  2. Delete the **devices** record in PocketBase. That also deletes its DNS
     record.

## DNS

The router hands out its own IPv6 DNS server, which doesn't know `home.arpa`.
Devices that ask it get "no such name" (PLAN.md D4). So the installer points
`home.arpa` lookups at the box, using the first method that matches the device.
Here `home.arpa` and `2.0.192.in-addr.arpa` (reverse 192.0.2.x) stand for
the domain and reverse zone the box reports; the installer skips this step if
it doesn't know them. `<box>` is the address given with `--box`.

| Device uses | What the installer does | Off this LAN |
|---|---|---|
| **systemd-resolved** (`/etc/resolv.conf` → `127.0.0.53`) | Writes `/etc/systemd/resolved.conf.d/hostupdate.conf`: `home.arpa` and the reverse zone go to `<box>` only. It then restarts resolved and flushes its cache | Other lookups are unaffected |
| **NetworkManager** (without resolved) | Sets `ipv6.ignore-auto-dns yes` on the connection that reaches the box and reapplies it. That connection's IPv4 DHCP DNS is already the box | Other connections are unaffected |
| **dhcpcd** (Slackware, Raspberry Pi OS, Arch) | Puts `nooption nd_rdnss` and `nooption dhcp6_name_servers` at the top of `/etc/dhcpcd.conf`, so dhcpcd drops the router's IPv6 DNS. DNS still comes from DHCP: the box at home. Restart networking or reboot afterwards | Unaffected: that network's DHCP DNS is used |
| **dhclient** (Debian ifupdown), **udhcpc** (Alpine, BusyBox), or no DHCP client (static) | Usually **nothing** (see below) | Unaffected |
| The same, when the router's IPv6 DNS does get in | Writes `/etc/resolv.conf` with only `nameserver <box>`, and stops the DHCP client rewriting it: the dhclient hook `/etc/dhcp/dhclient-enter-hooks.d/hostupdate`, or `RESOLV_CONF=no` in `/etc/udhcpc/udhcpc.conf` | **All DNS fails**; use `--no-dns` on laptops |
| **OpenBSD** with resolvd (the default) | Adds `forwarder { <box> }` and `force forwarder { home.arpa 2.0.192.in-addr.arpa }` to `/etc/unwind.conf` (checked with `unwind -n`), then enables unwind. resolvd points resolv.conf at it | Other lookups are unaffected |

Notes:
- **When "nothing" applies.** dhclient, udhcpc and static setups get IPv6
  from the kernel, which ignores the router's DNS. So the installer changes
  nothing unless the router's IPv6 DNS can get in. That's the case when
  `/etc/resolv.conf` already lists an IPv6 nameserver, or when `rdnssd`,
  `dhclient -6` or `udhcpc6` is running. Once it has pinned resolv.conf, it
  keeps it pinned on re-runs.
- **When resolv.conf is pinned.** The original is kept as
  `/etc/resolv.conf.hostupdate-orig`. Restart networking or reboot afterwards
  so the DHCP client rereads its config.
- **dhcpcd option names.** They come from dhcpcd's own definitions. The
  installer warns if `dhcpcd -V` doesn't list `rdnss`. To check by hand, run
  `dhcpcd -V | grep -E 'rdnss|name_servers'`. After the restart,
  `/etc/resolv.conf` should have no `fe80::` nameserver.
- **Upgrades.** A device that the first version of the installer pinned for
  dhcpcd gets the pin undone and the `nooption` lines instead.
- **Cases the installer skips.** It explains each one and changes nothing:
  - `/etc/resolv.conf` is a symlink to something other than resolved, e.g.
    Debian's `resolvconf`.
  - The device uses connman.
  - An `unwind.conf` already has its own forwarders. unwind would mix them
    into the `home.arpa` lookups.
  - resolvd is off on OpenBSD. If its static resolv.conf already lists the box
    first, that's reported as fine.

  Do these by hand: the recipes are in `RUNBOOK.md` §8b.
- **Checking.** Use `getent hosts <name>.home.arpa`, which uses the same path
  as apps, or `resolvectl query`. `dig` and `nslookup` can bypass these
  settings.

## Troubleshooting

| Message | Meaning and fix |
|---|---|
| `server rejected the name or password for '<name>'` | The **devices** record is missing or disabled, or its name or password doesn't match `/etc/hostupdate.conf`. Re-paste the password, or fix the name |
| `server says this device's source address … isn't a LAN client address` | The check-in came from outside the box's LAN subnet, or from the box's, the gateway's, the network or the broadcast address. For example, the device is behind NAT or on a VPN |
| `conflict …` | A hand-made **hosts** record with a PTR holds this device's IP, or another device's record or an alias uses this name. Fix, rename or delete that record. Hand-made records should use the lower half of the subnet (.2–.127 on a /24), outside the DHCP pool |
| `site: unknown` / `dns: skipped: the domain is unknown` (installer) | The installer couldn't get the domain from `…/api/hostupdate/info`, and none was saved or given. Re-run once the box answers, or pass `--domain` |
| `cannot reach http://<box>:8090/api/hostupdate` | No answer from PocketBase. Check that `curl http://<box>:8090/api/health` works from the device. If the box has moved, run `install.sh --box <new address>` |
| `… has no url=: run install.sh --box …`, or the installer asks `which box?` | The config predates `--box` and never saved the box's address. Run `install.sh --box <box>` |
| `server error (…)` | PocketBase failed. Look at its logs in the admin page (**Logs**) |
| `no HTTP client: install curl or wget` | None of the supported tools is installed |
| `cannot read /etc/hostupdate.conf` | It was run as a user other than root or the run-as user, or the config is gone. Run `install.sh` again |
| Check-ins work by hand, but the name stops resolving after a day, and the cron log shows `ERROR chdir failed (/nonexistent)` | cronie (Fedora, RHEL) won't start a job if the user's home is missing, which older installs had. Run the current `install.sh` again: it changes the home to `/` |
| `user: couldn't change its home …` (installer) | A check-in was running, so try again; or the device has no `usermod`, as on BusyBox. Then set the user's home to `/` by hand if the check-ins don't run. Check with `crontab -u hostupdate -l` and the cron log |

Notes:
- **Several LAN addresses.** A device with more than one LAN address, such as
  Wi-Fi plus Ethernet, or a bridge or VM interface, is registered with
  whichever one it uses to reach the box: a laptop with a VM interface on
  the LAN may be served as that interface's address.
- **HTTP tool order.** The client uses the first one available of curl, GNU
  wget, OpenBSD `ftp(1)` and BusyBox wget. curl and GNU wget keep the password
  out of `ps`. The other two put it in the request URL, visible to local users
  for the length of the request. Install curl if that matters on a device. Set
  `HOSTUPDATE_CLIENT=curl|gnuwget|ftp|bbwget|"busybox wget"` to force one when
  debugging.
- **Plain HTTP.** Check-ins use plain HTTP on the LAN (PLAN.md D18). Each
  device's password can only update that device's own name.
