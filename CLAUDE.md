# HomeSubnetDns

DNS (Unbound + PocketBase + dnssync) and DHCP (dhcpd) for a home LAN, and
a list of the machines on it (PocketBase `online_machines`, D44). They
run on an OpenBSD host, `dns.<DOMAIN>`, at `ADDRESS` in `site.conf`. Other
devices can keep their own `<name>.<DOMAIN>` record current with the
`hostupdate/` client (RUNBOOK §8a); the person installs it on each device.

This repository is public. A site's own details (its addresses, the box's
user, device names, the router, where to push) live only in `site.conf`
(from `site.conf.example`) and `CLAUDE.local.md`, which git ignores. Read
`CLAUDE.local.md` when it exists.

- **`RUNBOOK.md`**: read this first. It covers setting up from scratch,
  redeploying, day-to-day operations, verification, and gotchas.
- **`PLAN.md`**: design decisions D1–D44, deviations from the plan (§5a), open items (§8), rollback (§7), device check-ins (§9), dotted names, aliases and the site domain (§10), rebuilding the box (§11), the network read from the box (§12), machines on the LAN (§13).

## Working rules
- The repo is the source of truth. Don't edit files on the box by hand. To
  deploy anything: `scripts/install-box --check` (builds, stages, shows the
  diffs), then `scripts/install-box` (RUNBOOK §3). Other jobs run with
  `scripts/rdoas jobs/NN-*.sh`.
  Commit what you changed, and push to `origin` (`CLAUDE.local.md` says how).
- Root actions go through `scripts/rdoas`, which needs `RDOAS_PASSFILE` (a mode
  0600 file outside the repo). Never put the doas password in the repo, a job,
  a commit message, or chat output.
- **The whole LAN depends on this box for DHCP and DNS.** Confirm with the user
  before rebooting it, or changing its network, pf, unbound or dhcpd settings.
  Never run the router's DHCP and dhcpd at the same time.
- Processes started in the background through `rdoas` die when SSH closes. Use
  the foreground or `at(1)`.
- PocketBase changes: `scripts/build` runs `go test ./...` first; the installer deploys.
- Never write the domain or anything about the network (addresses, subnets,
  netmasks, ranges, the gateway, the interface name) in a deployable file or
  in code. `site.conf` has `DOMAIN` and `ADDRESS`; the installer reads the
  rest from the box's LAN interface and fills in `@DOMAIN@ @ADDRESS@
  @NETMASK@ @BITS@ @NETWORK@ @SUBNET@ @GATEWAY@ @RANGES@` on the box (D41).
  Code takes them from flags (`--dns-domain`, `--lan`, `--gateway`); devices
  from `install.sh --box`. Applying a `site.conf` change is a run of the
  installer, which needs the person's OK like any unbound or dhcpd change.
- **Nothing about a real site goes in a tracked file, or in a commit
  message**, because the repo is public: no real addresses, subnets, MACs,
  host, device or user names, router models, or personal details. Docs and
  tests use the documentation addresses (192.0.2.0/24, 198.51.100.0/24,
  RFC 5737), `home.arpa`, and placeholders such as `<user>` and `<ADDRESS>`.
  This site's facts go in `CLAUDE.local.md`.
- Don't commit `site.conf`, `CLAUDE.local.md`, `resources/` (local screenshots
  and notes) or `pocketbase/pocketbase` (build output).
- A new or replacement box is set up with `scripts/install-box`, or on the box
  itself from a `scripts/bundle` file (RUNBOOK §5). Without `--check` the
  installer changes the box it runs on, so ask before running it on the live one. `scripts/backup` files hold every record and the password
  hashes: keep them out of the repo and chat.
