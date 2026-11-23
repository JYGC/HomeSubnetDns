#!/bin/sh
#
# install.sh: set up hostupdate on this device, OpenBSD or Linux (PLAN.md §9).
# Run as root from a copy of this directory. Safe to run again on a device that
# already has it: it changes only what differs, keeps the password and the
# check-in minute, and asks for the PocketBase record only while check-ins fail.
#
# usage: sh install.sh [--box ADDRESS] [--no-dns] [--domain DOMAIN] [NAME]
#        sh install.sh --uninstall
#
#   --box ADDRESS    the DNS box's IPv4 address (site.conf ADDRESS). Needed the
#                    first time, and after the box moves; otherwise the one
#                    saved in /etc/hostupdate.conf (url=) is used
#   NAME             the devices name; defaults to the configured one, else the
#                    short hostname
#   --no-dns         leave this device's DNS settings alone
#   --domain DOMAIN  the box's domain, used only if the box doesn't answer;
#                    normally the box says (GET .../api/hostupdate/info)
#
# Sets up the run-as user, the client, /etc/hostupdate.conf (generated
# password), an hourly and at-boot check-in (cron, else a systemd timer), and
# DNS so that lookups in the box's domain reach the box (README.md "DNS"). DNS
# changes are recorded in /etc/hostupdate.state so --uninstall can undo them.

set -eu
PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin
umask 022

MARK="# hostupdate: installed by install.sh"
BEGIN="# BEGIN hostupdate"
END="# END hostupdate"

BIN=/usr/local/libexec/hostupdate
CONF=/etc/hostupdate.conf
STATE=/etc/hostupdate.state
UNITS=/etc/systemd/system
RESOLV=/etc/resolv.conf
RESOLVED_DROPIN=/etc/systemd/resolved.conf.d/hostupdate.conf
DHCPCD_CONF=/etc/dhcpcd.conf
DHCLIENT_HOOK=/etc/dhcp/dhclient-enter-hooks.d/hostupdate
UDHCPC_CONF=/etc/udhcpc/udhcpc.conf
UNWIND_CONF=/etc/unwind.conf

usage="usage: sh install.sh [--box ADDRESS] [--no-dns] [--domain DOMAIN] [NAME] | sh install.sh --uninstall"
say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$1" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
running() { pgrep -x "$1" >/dev/null 2>&1; }

# --- file helpers

# put FILE MODE <content: write FILE only if the content differs. An existing
# file keeps its owner and mode. Succeeds only if FILE changed.
put() {
	_new=$(mktemp) || exit 1
	cat >"$_new"
	if [ -f "$1" ] && cmp -s "$_new" "$1"; then
		rm -f "$_new"
		return 1
	fi
	if [ -f "$1" ]; then
		if ! cat "$_new" >"$1"; then
			rm -f "$_new"
			die "cannot write $1"
		fi
		rm -f "$_new"
	else
		mkdir -p "$(dirname "$1")"
		chmod "$2" "$_new"
		mv -f "$_new" "$1"
	fi
}

# without_block FILE: print FILE without the hostupdate block.
without_block() {
	[ -f "$1" ] || return 0
	awk -v b="$BEGIN" -v e="$END" '$0 == b { skip = 1 } !skip { print } $0 == e { skip = 0 }' "$1"
}

# set_block FILE MODE [top] <lines: put the lines between the markers at the
# end of FILE, or at the start with "top". Succeeds only if FILE changed.
set_block() {
	_block=$(cat)
	if [ "${3-}" = top ]; then
		{ printf '%s\n%s\n%s\n' "$BEGIN" "$_block" "$END"; without_block "$1"; } | put "$1" "$2"
	else
		{ without_block "$1"; printf '%s\n%s\n%s\n' "$BEGIN" "$_block" "$END"; } | put "$1" "$2"
	fi
}

# del_block FILE: remove the hostupdate block, and FILE too if nothing else is left.
del_block() {
	[ -f "$1" ] && grep -qxF "$BEGIN" "$1" || return 0
	if [ -z "$(without_block "$1")" ]; then
		rm -f "$1"
	else
		without_block "$1" | put "$1" 644 || true
	fi
}

# --- state: what this script changed in DNS, as key=value lines

state() {
	[ -f "$STATE" ] || return 0
	sed -n "s/^$1=//p" "$STATE" | head -n 1
}
state_set() {
	{ [ ! -f "$STATE" ] || grep -v "^$1=" "$STATE" || true; printf '%s=%s\n' "$1" "$2"; } |
		put "$STATE" 600 || true
}
state_unset() {
	[ -f "$STATE" ] || return 0
	{ grep -v "^$1=" "$STATE" || true; } | put "$STATE" 600 || true
}

# --- DNS: make lookups in $zones reach the box (README.md "DNS", PLAN.md D26-D28)

lan_dev() { ip route get "$BOX" 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -n 1; }

linux_dns_method() {
	if grep -q '^nameserver 127\.0\.0\.53' "$RESOLV" 2>/dev/null && have resolvectl; then
		echo resolved
	elif running NetworkManager && have nmcli; then
		echo nm
	elif [ -L "$RESOLV" ] || running NetworkManager || running connmand; then
		echo unknown
	elif running dhcpcd; then
		echo dhcpcd
	elif running dhclient; then
		echo dhclient
	elif running udhcpc; then
		echo udhcpc
	else
		echo static
	fi
}

# systemd-resolved: route only the domain and the reverse zone to the box.
apply_resolved() {
	routes=$(printf '~%s ' $zones)
	if printf '%s\n' "$MARK" '[Resolve]' "DNS=$BOX" "Domains=${routes% }" |
		put "$RESOLVED_DROPIN" 644; then
		systemctl restart systemd-resolved
		resolvectl flush-caches 2>/dev/null || true
		say "dns: systemd-resolved now sends $domain lookups to $BOX ($RESOLVED_DROPIN)"
	else
		say "dns: unchanged (systemd-resolved sends $domain lookups to $BOX)"
	fi
	state_set dns resolved
}
revert_resolved() {
	[ -e "$RESOLVED_DROPIN" ] || return 0
	rm -f "$RESOLVED_DROPIN"
	systemctl restart systemd-resolved
	say "dns: removed $RESOLVED_DROPIN"
}

# NetworkManager: the connection that reaches the box stops using the router's
# IPv6 DNS; its IPv4 DHCP DNS is already the box. Other connections keep theirs.
apply_nm() {
	dev=$(lan_dev)
	uuid=
	if [ -n "$dev" ]; then
		uuid=$(nmcli -t -f UUID,DEVICE connection show --active |
			sed -n "s/^\([^:]*\):$dev\$/\1/p" | head -n 1)
	fi
	if [ -z "$uuid" ]; then
		say "dns: skipped: no active NetworkManager connection reaches $BOX (README.md \"DNS\")"
		return 0
	fi
	if [ "$(state nm_conn)" != "$uuid" ]; then
		revert_nm
		state_set nm_conn "$uuid"
		state_set nm_prev "$(nmcli -g ipv6.ignore-auto-dns connection show "$uuid")"
	fi
	conn=$(nmcli -g connection.id connection show "$uuid")
	if [ "$(nmcli -g ipv6.ignore-auto-dns connection show "$uuid")" = yes ]; then
		say "dns: unchanged (NetworkManager connection '$conn' ignores the router's IPv6 DNS)"
	else
		nmcli connection modify "$uuid" ipv6.ignore-auto-dns yes
		if nmcli device reapply "$dev" >/dev/null 2>&1; then
			say "dns: NetworkManager connection '$conn' now ignores the router's IPv6 DNS"
		else
			say "dns: NetworkManager connection '$conn' now ignores the router's IPv6 DNS; reconnect it to apply"
		fi
	fi
	state_set dns nm
}
# Shell variables are global: revert_* run inside apply_*, so they use their own names.
revert_nm() {
	_ruuid=$(state nm_conn)
	[ -n "$_ruuid" ] || return 0
	if [ "$(state nm_prev)" != yes ] &&
		nmcli connection modify "$_ruuid" ipv6.ignore-auto-dns no 2>/dev/null; then
		_rdev=$(nmcli -g GENERAL.DEVICES connection show "$_ruuid" 2>/dev/null || true)
		[ -z "$_rdev" ] || nmcli device reapply "$_rdev" >/dev/null 2>&1 || true
		say "dns: NetworkManager connection $_ruuid uses the router's IPv6 DNS again"
	fi
	state_unset nm_conn
	state_unset nm_prev
}

# dhcpcd: drop the router's IPv6 DNS (RDNSS in router advertisements, and
# DHCPv6) before dhcpcd uses it. DNS still comes from DHCP: the box at home,
# and that network's server elsewhere, so it is safe off this LAN (D27).
apply_dhcpcd() {
	# At the top: options after an "interface" line apply to that interface only.
	if printf 'nooption nd_rdnss\nnooption dhcp6_name_servers\n' | set_block "$DHCPCD_CONF" 644 top; then
		say "dns: dhcpcd now ignores the router's IPv6 DNS ($DHCPCD_CONF); restart networking or reboot so dhcpcd rereads its config"
	else
		say "dns: unchanged (dhcpcd ignores the router's IPv6 DNS)"
	fi
	if ! dhcpcd -V 2>/dev/null | grep -q rdnss; then
		say "  warning: 'dhcpcd -V' doesn't list rdnss; check the option names (README.md \"DNS\")"
	fi
	state_set dns dhcpcd
}
revert_dhcpcd() {
	grep -qxF "$BEGIN" "$DHCPCD_CONF" 2>/dev/null || return 0
	del_block "$DHCPCD_CONF"
	say "dns: removed the hostupdate lines from $DHCPCD_CONF; dhcpcd uses the router's IPv6 DNS again after a restart"
}

# dhclient, udhcpc and static setups get IPv6 from the kernel's SLAAC, which
# ignores router DNS. The router's IPv6 DNS only reaches resolv.conf through
# one of these helpers, or if it's already listed there.
router_v6_dns() {
	grep -Eq '^nameserver[[:space:]]+[0-9A-Fa-f.]*:' "$RESOLV" 2>/dev/null && return 0
	running rdnssd && return 0
	case $1 in
	dhclient) pgrep -f 'dhclient.* -6' >/dev/null 2>&1 ;;
	udhcpc) running udhcpc6 ;;
	*) return 1 ;;
	esac
}

# Only when router_v6_dns says so: the box becomes the only nameserver, and the
# DHCP client is told to leave resolv.conf alone (D27).
apply_plain() {
	hooked=false
	case $1 in
	dhclient)
		if printf '%s\nmake_resolv_conf() { :; }\n' "$MARK" | put "$DHCLIENT_HOOK" 644; then hooked=true; fi
		;;
	udhcpc)
		if printf 'RESOLV_CONF=no\n' | set_block "$UDHCPC_CONF" 644; then hooked=true; fi
		;;
	esac
	if [ -f "$RESOLV" ] && [ ! -e "$RESOLV.hostupdate-orig" ] && ! grep -qxF "$MARK" "$RESOLV"; then
		cp -p "$RESOLV" "$RESOLV.hostupdate-orig"
	fi
	if printf '%s\nsearch %s\nnameserver %s\n' "$MARK" "$domain" "$BOX" | put "$RESOLV" 644 || $hooked; then
		if [ "$1" = static ]; then
			say "dns: $RESOLV now lists only $BOX"
		else
			say "dns: $RESOLV now lists only $BOX, and $1 no longer rewrites it; restart networking or reboot so $1 rereads its config"
		fi
	else
		say "dns: unchanged ($RESOLV lists only $BOX)"
	fi
	state_set plain "$1"
	state_set dns plain
}
revert_plain() {
	_rplain=$(state plain)
	case $_rplain in
	dhcpcd) del_block "$DHCPCD_CONF" ;; # pinned by the first version of this script
	dhclient) rm -f "$DHCLIENT_HOOK" ;;
	udhcpc) del_block "$UDHCPC_CONF" ;;
	esac
	# Restore the saved file only while resolv.conf is still ours; if something
	# else has rewritten it since (e.g. systemd-resolved), leave that alone.
	if [ -e "$RESOLV.hostupdate-orig" ] && ! grep -qxF "$MARK" "$RESOLV" 2>/dev/null; then
		rm -f "$RESOLV.hostupdate-orig"
	elif [ -e "$RESOLV.hostupdate-orig" ]; then
		cat "$RESOLV.hostupdate-orig" >"$RESOLV"
		rm -f "$RESOLV.hostupdate-orig"
		if [ "$_rplain" = static ]; then
			say "dns: restored the previous $RESOLV"
		else
			say "dns: restored the previous $RESOLV; restart networking or reboot so $_rplain manages it again"
		fi
	fi
	state_unset plain
}

# OpenBSD: unwind(8) forces the zones to the box; resolvd points resolv.conf at
# unwind. Other lookups are unchanged, so it is safe off this LAN (D28).
apply_unwind() {
	if ! running resolvd; then
		if [ "$(sed -n 's/^nameserver[[:space:]]*//p' "$RESOLV" 2>/dev/null | head -n 1)" = "$BOX" ]; then
			say "dns: unchanged ($RESOLV is static and lists $BOX first)"
		else
			say "dns: skipped: resolvd is off, so unwind wouldn't be used; put 'nameserver $BOX' first in $RESOLV"
		fi
		return 0
	fi
	if without_block "$UNWIND_CONF" | grep -Eq '^[[:space:]]*forwarder'; then
		# unwind merges all forwarders into one list, which "force" would use too.
		say "dns: skipped: $UNWIND_CONF already has forwarders, which $domain lookups would also use (README.md \"DNS\")"
		return 0
	fi
	cand=$(mktemp)
	{
		without_block "$UNWIND_CONF"
		printf '%s\n' "$BEGIN" "forwarder { $BOX }" "force forwarder { $zones }" "$END"
	} >"$cand"
	if ! unwind -n -f "$cand" >/dev/null 2>&1; then
		rm -f "$cand"
		say "dns: skipped: unwind -n rejected $UNWIND_CONF with the hostupdate lines added"
		return 0
	fi
	if [ -z "$(state unwind_conf)" ]; then
		if [ -e "$UNWIND_CONF" ]; then state_set unwind_conf merged; else state_set unwind_conf created; fi
		if rcctl ls on | grep -qx unwind; then state_set unwind_enabled yes; else state_set unwind_enabled no; fi
	fi
	changed=false
	if put "$UNWIND_CONF" 644 <"$cand"; then changed=true; fi
	rm -f "$cand"
	rcctl enable unwind
	if $changed || ! rcctl check unwind >/dev/null 2>&1; then
		rcctl restart unwind >/dev/null
		say "dns: unwind now sends $domain lookups to $BOX; resolvd points $RESOLV at it"
	else
		say "dns: unchanged (unwind sends $domain lookups to $BOX)"
	fi
	state_set dns unwind
}
revert_unwind() {
	case $(state unwind_conf) in
	created) rm -f "$UNWIND_CONF" ;;
	merged) del_block "$UNWIND_CONF" ;;
	*) return 0 ;;
	esac
	if [ "$(state unwind_enabled)" = no ]; then
		rcctl stop unwind >/dev/null 2>&1 || true
		rcctl disable unwind
	else
		rcctl restart unwind >/dev/null 2>&1 || true
	fi
	say "dns: removed the hostupdate lines from unwind"
	state_unset unwind_conf
	state_unset unwind_enabled
}

revert_dns() {
	case $(state dns) in
	resolved) revert_resolved ;;
	nm) revert_nm ;;
	dhcpcd) revert_dhcpcd ;;
	plain) revert_plain ;;
	unwind) revert_unwind ;;
	esac
	state_unset dns
}

configure_dns() {
	if [ "$os" = OpenBSD ]; then method=unwind; else method=$(linux_dns_method); fi
	case $method in
	dhclient | udhcpc | static)
		# Once pinned, stay pinned: the pinned file hides the evidence.
		if { [ "$(state dns)" = plain ] && [ "$(state plain)" = "$method" ]; } || router_v6_dns "$method"; then
			kind=plain
		else
			kind=none
		fi
		;;
	*) kind=$method ;;
	esac
	# The device's network setup may have changed since the last run.
	prev=$(state dns)
	if [ -n "$prev" ] && [ "$prev" != "$kind" ]; then revert_dns; fi
	if [ "$kind" = plain ] && [ -n "$(state plain)" ] && [ "$(state plain)" != "$method" ]; then
		revert_plain
	fi
	case $kind in
	resolved) apply_resolved ;;
	nm) apply_nm ;;
	dhcpcd) apply_dhcpcd ;;
	plain) apply_plain "$method" ;;
	none) say "dns: nothing to change (no IPv6 nameserver in $RESOLV, and nothing here passes on the router's IPv6 DNS)" ;;
	unwind) apply_unwind ;;
	*) say "dns: skipped: can't tell what manages $RESOLV (README.md \"DNS\" has the manual steps)" ;;
	esac
}

# --- arguments

dns=true
uninstall=false
NAME=
opt_domain=
opt_box=
while [ $# -gt 0 ]; do
	case $1 in
	--no-dns) dns=false ;;
	--uninstall) uninstall=true ;;
	--box) [ $# -ge 2 ] || die "--box needs the box's address ($usage)"; opt_box=$2; shift ;;
	--box=*) opt_box=${1#--box=} ;;
	--domain) [ $# -ge 2 ] || die "--domain needs a value ($usage)"; opt_domain=$2; shift ;;
	--domain=*) opt_domain=${1#--domain=} ;;
	-h | --help) say "$usage"; exit 0 ;;
	-*) die "unknown option $1 ($usage)" ;;
	*) [ -z "$NAME" ] || die "only one NAME ($usage)"; NAME=$1 ;;
	esac
	shift
done
label='[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?'
if [ -n "$opt_domain" ] && ! echo "$opt_domain" | grep -Eqx "$label(\.$label)*"; then
	die "'$opt_domain' isn't a domain name, e.g. home.arpa"
fi
octet='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
is_ip() { echo "$1" | grep -Eqx "($octet\.){3}$octet"; }
if [ -n "$opt_box" ] && ! is_ip "$opt_box"; then
	die "--box takes the box's IPv4 address, e.g. --box 192.0.2.53"
fi

[ "$(id -u)" = 0 ] || die "run as root"
os=$(uname -s)
case $os in
OpenBSD) user=_hostupdate ;;
Linux) user=hostupdate ;;
*) die "unsupported OS: $os" ;;
esac

# --- uninstall

# The box: --box, else the url= saved by an earlier run. The admin page is on
# the same server as the check-ins.
saved_url=$(sed -n 's/^url=//p' "$CONF" 2>/dev/null | head -n 1)
if [ -n "$opt_box" ]; then url=http://$opt_box:8090/api/hostupdate; else url=$saved_url; fi
if [ -n "$url" ]; then admin=${url%/api/hostupdate}/_/; else admin="the PocketBase admin page"; fi

if $uninstall; then
	revert_dns
	if have crontab; then crontab -u "$user" -r 2>/dev/null || true; fi
	if [ -e "$UNITS/hostupdate.timer" ]; then
		systemctl disable --now hostupdate.timer 2>/dev/null || true
		rm -f "$UNITS/hostupdate.timer" "$UNITS/hostupdate.service"
		systemctl daemon-reload
	fi
	rm -f "$BIN" "$CONF" "$STATE"
	if id "$user" >/dev/null 2>&1; then
		# Never userdel -r: on Linux the user's home is /.
		if have userdel; then userdel "$user"; else deluser "$user"; fi
	fi
	say "Removed hostupdate. Delete this device's record in PocketBase ($admin, devices); that also deletes its DNS record."
	exit 0
fi

cd "$(dirname "$0")"
[ -f hostupdate ] || die "run from the hostupdate directory (hostupdate not found)"
[ -n "$url" ] || die "which box? Pass --box with the DNS box's address, e.g. --box 192.0.2.53 (site.conf ADDRESS)"
BOX=$(printf '%s\n' "$url" | sed -n 's|^http://\([0-9.]*\):[0-9]*/.*|\1|p')
is_ip "$BOX" || die "url=$url in $CONF doesn't name the box by its IPv4 address: run again with --box"
saved_box=$(printf '%s\n' "$saved_url" | sed -n 's|^http://\([0-9.]*\):[0-9]*/.*|\1|p')
if [ -z "$saved_box" ]; then
	say "box: $BOX"
elif [ "$saved_box" != "$BOX" ]; then
	say "box: $BOX (was $saved_box)"
else
	say "box: $BOX (saved in $CONF)"
fi

# --- run-as user

# The home must exist: cronie (Fedora, RHEL) won't start a job without it. Linux
# has no one directory like OpenBSD's /var/empty, so use /: the user can't write
# there. Earlier installs used /nonexistent; put those right.
home_changed=false
if id "$user" >/dev/null 2>&1; then
	say "user: $user exists"
	home=$(grep "^$user:" /etc/passwd | cut -d: -f6)
	if [ -n "$home" ] && [ ! -d "$home" ]; then
		if have usermod && usermod -d / "$user"; then
			home_changed=true
			say "user: changed its home from $home to /, so that cron can run its jobs"
		else
			say "user: couldn't change its home from $home to /, and cron may not run its jobs: try again, or change it by hand"
		fi
	fi
else
	if [ "$os" = OpenBSD ]; then
		useradd -c "hostupdate client" -d /var/empty -s /sbin/nologin -g =uid "$user"
	else
		nologin=$(command -v nologin || echo /bin/false)
		if have useradd; then
			useradd --system --user-group --no-create-home --home-dir / \
				--shell "$nologin" --comment "hostupdate client" "$user"
		else
			adduser -S -D -H -h / -s "$nologin" -g "hostupdate client" "$user"
		fi
	fi
	say "user: created $user"
fi

# --- client

if [ -f "$BIN" ] && cmp -s hostupdate "$BIN"; then
	say "client: unchanged ($BIN)"
else
	mkdir -p "$(dirname "$BIN")"
	cp hostupdate "$BIN.tmp"
	chown root "$BIN.tmp"
	chmod 555 "$BIN.tmp"
	mv -f "$BIN.tmp" "$BIN"
	say "client: installed $BIN"
fi

# --- site: the domain and reverse zone, from the box (D32)

old_name= password= old_domain= old_reverse= extra=
if [ -f "$CONF" ]; then
	old_name=$(sed -n 's/^name=//p' "$CONF" | head -n 1)
	password=$(sed -n 's/^password=//p' "$CONF" | head -n 1)
	old_domain=$(sed -n 's/^domain=//p' "$CONF" | head -n 1)
	old_reverse=$(sed -n 's/^reverse=//p' "$CONF" | head -n 1)
	extra=$(grep -v -e '^name=' -e '^password=' -e '^domain=' -e '^reverse=' -e '^url=' "$CONF" || true)
fi
info=$(HOSTUPDATE_URL=$url HOSTUPDATE_CONF=$CONF "$BIN" --info 2>/dev/null || true)
domain=$(printf '%s\n' "$info" | sed -n 's/^domain=//p' | head -n 1)
reverse=$(printf '%s\n' "$info" | sed -n 's/^reverse=//p' | head -n 1)
if [ -n "$domain" ]; then
	say "site: $domain (from the box)"
	if [ -n "$opt_domain" ] && [ "$opt_domain" != "$domain" ]; then
		say "  note: ignored --domain $opt_domain; the box serves $domain"
	fi
elif [ -n "$opt_domain" ]; then
	domain=$opt_domain reverse=$old_reverse
	say "site: $domain (--domain; the box didn't answer at $url/info)"
elif [ -n "$old_domain" ]; then
	domain=$old_domain reverse=$old_reverse
	say "site: $domain (saved in $CONF; the box didn't answer at $url/info)"
else
	say "site: unknown: the box didn't answer at $url/info; re-run when it does, or pass --domain"
fi
[ -n "$domain" ] && [ -n "$info" ] ||
	say "  if the box has moved, run this again with --box and its new address (site.conf ADDRESS)"
zones="$domain${reverse:+ $reverse}"
fqdn() { printf '%s%s' "$1" "${domain:+.$domain}"; }

# --- config: keep the name, password and any other lines already there

name=${NAME:-${old_name:-$(uname -n | cut -d. -f1 | tr 'A-Z' 'a-z')}}
echo "$name" | grep -Eq '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' ||
	die "'$name' isn't a valid name: lowercase letters, digits and inner hyphens, at most 63"
case $name in dns | pocketbase | router) die "'$name' is reserved on the box: pass another NAME" ;; esac
if [ -z "$password" ]; then
	password=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | fold -w 32 | head -n 1)
fi
[ ${#password} -ge 32 ] || die "the password in $CONF is shorter than 32 characters"
if {
	printf 'name=%s\npassword=%s\n' "$name" "$password"
	[ -z "$domain" ] || printf 'domain=%s\n' "$domain"
	[ -z "$reverse" ] || printf 'reverse=%s\n' "$reverse"
	printf 'url=%s\n' "$url"
	[ -z "$extra" ] || printf '%s\n' "$extra"
} | put "$CONF" 400; then
	if [ -z "$old_name" ]; then
		say "config: wrote $CONF with a new password"
	elif [ "$old_name" != "$name" ]; then
		say "config: name changed from '$old_name' to '$name'; rename its devices record in PocketBase to match"
	else
		say "config: updated $CONF (password kept)"
	fi
else
	say "config: unchanged ($CONF, password kept)"
fi
chown "$user" "$CONF"
chmod 400 "$CONF"

# --- schedule: hourly at a fixed random minute, plus once at boot

if have crontab; then
	cur=$(crontab -u "$user" -l 2>/dev/null || true)
	minute=$(printf '%s\n' "$cur" | sed -n "s|^\([0-9][0-9]*\) \* \* \* \* $BIN\$|\1|p" | head -n 1)
	[ -n "$minute" ] || minute=$(($(od -An -N2 -tu2 /dev/urandom | tr -d ' ') % 60))
	# Drop our old lines, blank lines, and the header some crons add to stored tabs.
	new=$(
		printf '%s\n' "$cur" | grep -v -e "$BIN" -e "^$MARK" -e '^$' \
			-e '^# DO NOT EDIT THIS FILE' -e '^# (.* installed on ' -e '^# (Cron version' || true
		printf '%s\n%s * * * * %s\n@reboot %s --boot\n' "$MARK" "$minute" "$BIN" "$BIN"
	)
	# cron keeps the home it saw when it read the crontab: writing it makes cron
	# read it again.
	if [ "$new" = "$cur" ] && ! $home_changed; then
		say "schedule: unchanged (cron, hourly at :$(printf '%02d' "$minute"), and at boot)"
	else
		printf '%s\n' "$new" | crontab -u "$user" -
		say "schedule: cron, hourly at :$(printf '%02d' "$minute"), and at boot (crontab -u $user -l)"
	fi
elif have systemctl && [ -d /run/systemd/system ]; then
	changed=false
	if put "$UNITS/hostupdate.service" 644 <<EOF
[Unit]
Description=Check in with the DNS box (hostupdate)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
User=$user
ExecStart=$BIN --boot
EOF
	then changed=true; fi
	if put "$UNITS/hostupdate.timer" 644 <<EOF
[Unit]
Description=Hourly check-in with the DNS box (hostupdate)

[Timer]
OnBootSec=1min
OnUnitActiveSec=1h
RandomizedDelaySec=5min

[Install]
WantedBy=timers.target
EOF
	then changed=true; fi
	if $changed; then systemctl daemon-reload; fi
	systemctl enable --now hostupdate.timer >/dev/null 2>&1
	if $changed; then
		say "schedule: systemd hostupdate.timer, hourly and 1 minute after boot"
	else
		say "schedule: unchanged (systemd hostupdate.timer)"
	fi
else
	die "no cron or systemd: run '$BIN' as $user hourly, and at boot with --boot, some other way"
fi

# --- DNS

if $dns && [ -z "$domain" ]; then
	say "dns: skipped: the domain is unknown (see site:)"
elif $dns; then
	configure_dns
elif [ -n "$(state dns)" ]; then
	say "dns: left as set up earlier (--no-dns); --uninstall undoes it"
else
	say "dns: not touched (--no-dns)"
fi

# --- check in; only ask for the PocketBase record while that fails

checkin() { su -s /bin/sh "$user" -c "$BIN" </dev/null; }

if checkin 2>/dev/null; then
	say "check-in: OK ($(fqdn "$name"))"
elif [ -z "$info" ]; then
	say "check-in: failed: the box doesn't answer at $url. If it has moved, run this again with --box and its new address; the scheduled check-ins keep trying"
else
	cat <<EOF

Check-ins don't work yet. In PocketBase ($admin), open the "devices"
collection and add a record (or fix the existing one) with:

  name:      $name
  password:  $password

Paste the password into both password fields. If a hand-made host record
named '$name' exists, the first check-in takes it over; if an alias has that
name, check-ins fail with "conflict" until you rename one of them.

EOF
	if [ -t 0 ]; then
		printf 'Press Enter when the record is saved to test a check-in (Ctrl-C skips the test): '
		if read -r _; then
			if checkin; then
				say "check-in: OK; $(fqdn "$name") resolves within a minute"
			else
				say "check-in: failed (see above); the scheduled check-ins keep trying"
			fi
		fi
	else
		say "check-in: the scheduled check-ins pick it up once the record exists"
	fi
fi
say "Logs go to syslog with the tag 'hostupdate'. To remove everything: sh install.sh --uninstall"
