# Set up this OpenBSD box as the DNS box, fresh or in place of a lost one, or
# bring an installed box up to date (RUNBOOK.md §5, §3): the address, the ARP
# timeout, Unbound, pf, PocketBase (optionally restored from a scripts/backup
# file), dnssync, the hostname, dhcpd and OS patches, then checks. Two ways to
# run it:
# - from the workstation: scripts/install-box builds and stages the files to
#   ~/.rdoas/stage of the user it logs in as, then runs this as root through
#   scripts/rdoas, which puts this job in ~/.rdoas
# - on the box itself: unpack a scripts/bundle file, which has this as
#   install-box/install.sh and the files in install-box/files, then
#   doas sh install-box/install.sh [options]
#
# The staged files are templates. Their @NAME@ placeholders are filled in here
# (D41): DOMAIN and ADDRESS come from site.conf; the netmask and the gateway
# from the LAN interface; the subnet, the DHCP pool and the reverse zone are
# worked out from those. Nothing about the network is written in the repo.
#
# It is idempotent: each step changes only what differs from what it should
# be, and restarts a service that runs but doesn't answer on ADDRESS. Run it
# again as often as you like; it stops with instructions where a person has
# to act: after scheduling an address switch, before dhcpd starts (that
# needs --start-dhcpd), and after a kernel patch.
#
# usage: sh install.sh [--check] [--restore FILE] [--start-dhcpd] [--interface IF] [--gateway IP]
#   --check        only report what would change; safe on the running box
#   --restore      a scripts/backup file: PocketBase's data and the DHCP leases
#   --start-dhcpd  the router's DHCP server is off and the old box is down
#   --interface    the LAN interface (default: the one with ADDRESS, else the
#                  one with the default route)
#   --gateway      the LAN's router (default: the default route, else the DHCP
#                  lease, else /etc/mygate); needed only when none of those is
#                  on the LAN, e.g. after the router moved the LAN
set -eu
opts="$*"
here=$(cd "$(dirname "$0")" && pwd)
if [ -r "$here/files/site.conf" ]; then
	S=$here/files	# unpacked from a bundle
	again="doas sh $here/$(basename "$0")"
else
	S=$here/stage	# scripts/stage's folder, next to this job (scripts/rdoas)
	again=
fi
E=/var/unbound/etc
H=$E/home.d
P=/var/pocketbase
N=/root/netswitch.new	# the settings an address switch moves to
# dnssync's PocketBase login. Only an identifier: .invalid never resolves (RFC 2606).
SA=dnssync@dns.invalid

check=no restore= start_dhcpd=no IF= GATEWAY=
usage() {
	echo "usage: $0 [--check] [--restore FILE] [--start-dhcpd] [--interface IF] [--gateway IP]" >&2
	exit 2
}
while [ $# -gt 0 ]; do
	case $1 in
	--check) check=yes ;;
	--start-dhcpd) start_dhcpd=yes ;;
	--restore) [ $# -ge 2 ] || usage; restore=$2; shift ;;
	--interface) [ $# -ge 2 ] || usage; IF=$2; shift ;;
	--gateway) [ $# -ge 2 ] || usage; GATEWAY=$2; shift ;;
	*) usage ;;
	esac
	shift
done
# A path from the directory it's run in (scripts/install-box gives one from the login directory).
case $restore in '' | /*) ;; *) restore=$PWD/$restore ;; esac

step() { echo "--- $*"; }
die() { echo "install-box: $*" >&2; exit 1; }
# How to run this again for real: the options it was given, less --check, plus $1.
rerun() {
	_o=$(echo " $opts " | sed 's/ --check / /g; s/^ *//; s/ *$//')
	if [ -n "$again" ]; then
		echo "$again${_o:+ $_o}${1:+ $1}"
	elif [ "$check" = yes ]; then
		echo "scripts/install-box on the workstation, with the same options but without --check${1:+, plus $1}"
	else
		echo "scripts/install-box on the workstation, with the same options${1:+ plus $1}"
	fi
}
pending=0	# changes made, or in --check mode changes that would be made

# Run a command, or in --check mode only say what would run.
act() {
	pending=$((pending + 1))
	if [ "$check" = yes ]; then
		echo "would run: $*"
		return 0
	fi
	"$@"
}

# put SRC DST MODE OWNER:GROUP installs SRC as DST unless DST already has that
# content and those permissions, showing what differs. True if it changed DST
# (or would).
put() {
	if [ -e "$2" ] && cmp -s "$1" "$2" && [ "$(stat -f '%Lp %Su:%Sg' "$2")" = "$3 $4" ]; then
		echo "unchanged: $2"
		return 1
	fi
	if [ ! -e "$2" ]; then
		echo "missing: $2"
	elif cmp -s "$1" "$2"; then
		echo "differs: $2 (owner or mode)"
	else
		echo "differs: $2"
		diff -u "$2" "$1" | sed -n '3,$p' | head -n 40 || true
	fi
	act install -m "$3" -o "${4%:*}" -g "${4#*:}" "$1" "$2"
}

# dir PATH MODE OWNER:GROUP does the same for a directory.
dir() {
	if [ -d "$1" ] && [ "$(stat -f '%Lp %Su:%Sg' "$1")" = "$2 $3" ]; then
		return 0
	fi
	act install -d -m "$2" -o "${3%:*}" -g "${3#*:}" "$1"
}

running() { rcctl check "$1" >/dev/null 2>&1; }
enabled() { rcctl ls on | grep -qx "$1"; }

# IPv4 addresses as numbers and back; OpenBSD's sh has 64-bit arithmetic.
is_ip() {
	echo "$1" | grep -Eqx '((25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
}
ip2n() {
	_ifs=$IFS
	IFS=.
	set -- $1
	IFS=$_ifs
	echo $((($1 << 24) + ($2 << 16) + ($3 << 8) + $4))
}
n2ip() { echo "$((($1 >> 24) & 255)).$((($1 >> 16) & 255)).$((($1 >> 8) & 255)).$(($1 & 255))"; }
default_gw() { route -n get -inet default 2>/dev/null | sed -n 's/^ *gateway: //p' | head -n 1; }

t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
R=$t/rendered
mkdir $R

step "preflight"
[ "$(id -u)" -eq 0 ] || die "run it as root: doas sh $0 $opts"
if [ "$(uname -s)" != OpenBSD ] || [ "$(uname -m)" != amd64 ]; then
	die "needs OpenBSD on amd64, which the PocketBase build is for"
fi
[ "$(uname -r)" = 8.0 ] || echo "warning: written for OpenBSD 8.0; this is $(uname -r)"
for f in site.conf hostname.if mygate netswitch.sh pf.conf unbound.conf static.conf \
    pocketbase rc.d.pocketbase dnssync dnssync.crontab dhcpd.conf; do
	[ -r $S/$f ] || die "$S/$f is missing: make the bundle again (scripts/bundle), or stage (scripts/install-box does)"
done
case $S in *[!A-Za-z0-9/._-]*) die "unpack the bundle where the path has no spaces or odd characters: $S" ;; esac
if [ -n "$again" ] && [ -r "$here/README.txt" ]; then head -n 1 "$here/README.txt"; fi
DOMAIN=$(sed -n 's/^DOMAIN=//p' $S/site.conf)
ADDRESS=$(sed -n 's/^ADDRESS=//p' $S/site.conf)
echo "$DOMAIN" | grep -Eqx '[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*' ||
	die "site.conf: DOMAIN must be a lowercase domain name, e.g. home.arpa"
is_ip "$ADDRESS" || die "site.conf: ADDRESS must be the box's IPv4 address, e.g. 192.0.2.53"
[ -z "$GATEWAY" ] || is_ip "$GATEWAY" || die "--gateway must be an IPv4 address, e.g. 192.0.2.1"
if [ -n "$restore" ] && [ ! -r "$restore" ]; then die "can't read $restore"; fi
if [ "$check" = yes ]; then mode="check only, nothing is changed"; else mode=install; fi
echo "OpenBSD $(uname -r), DOMAIN=$DOMAIN ADDRESS=$ADDRESS ($mode)"

step "the LAN, as the box sees it (D41)"
# The interface: the one that already has ADDRESS, else the one with the default route.
if [ -z "$IF" ]; then
	IF=$(ifconfig | awk -v a="$ADDRESS" '/^[a-z]/ { i = $1; sub(/:$/, "", i) } $1 == "inet" && $2 == a { print i; exit }')
fi
[ -n "$IF" ] || IF=$(ifconfig egress 2>/dev/null | sed -n 's/^\([a-z][a-z0-9]*\): flags=.*/\1/p' | head -n 1)
case $IF in
'' | *[!a-z0-9]*) die "can't tell which interface is the LAN (nothing has $ADDRESS, and no default route): pass --interface" ;;
esac
ifconfig "$IF" >/dev/null 2>&1 || die "no interface $IF"

# The netmask: from ADDRESS on the interface, else from the address it has on
# the same network (DHCP, or the old static one).
an=$(ip2n $ADDRESS)
addrs=$(ifconfig "$IF" | sed -n 's/^[[:space:]]*inet \([0-9.]*\) netmask \(0x[0-9a-fA-F]*\).*/\1 \2/p')
[ -n "$addrs" ] || die "$IF has no IPv4 address, so the LAN's netmask is unknown. Put the box on the LAN first: by DHCP (ifconfig $IF inet autoconf), or with an address and netmask, then run this again"
cur= mask=
set -- $addrs
while [ $# -ge 2 ]; do
	m=$(($2))
	if [ $(($(ip2n $1) & m)) -eq $((an & m)) ]; then
		cur=$1 mask=$m
		[ "$1" != "$ADDRESS" ] || break
	fi
	shift 2
done
if [ -z "$cur" ]; then
	die "ADDRESS=$ADDRESS (site.conf) isn't on the network $IF is on ($(echo $addrs | cut -d' ' -f1-2)). Fix ADDRESS, or put the box on the LAN first (DHCP: ifconfig $IF inet autoconf), then run this again"
fi
bits=0 m=$mask
while [ $((m & 0x80000000)) -ne 0 ]; do bits=$((bits + 1)) m=$(((m << 1) & 0xffffffff)); done
[ $m -eq 0 ] || die "$IF has an odd netmask ($(n2ip $mask))"
[ $bits -ge 16 ] && [ $bits -le 29 ] || die "the LAN is a /$bits: only /16 to /29 are supported"
net=$((an & mask))
bcast=$((net | (~mask & 0xffffffff)))
NETWORK=$(n2ip $net) NETMASK=$(n2ip $mask) BROADCAST=$(n2ip $bcast) BITS=$bits SUBNET=$(n2ip $net)/$bits
[ $an -ne $net ] && [ $an -ne $bcast ] || die "ADDRESS=$ADDRESS is the network or broadcast address of $SUBNET"

# The gateway: --gateway, else the default route, the DHCP lease, /etc/mygate;
# the first that is on the LAN.
on_lan() {
	is_ip "$1" && [ $(($(ip2n $1) & mask)) -eq $net ] &&
		[ "$1" != "$ADDRESS" ] && [ "$1" != "$NETWORK" ] && [ "$1" != "$BROADCAST" ]
}
if [ -n "$GATEWAY" ]; then
	on_lan "$GATEWAY" || die "--gateway $GATEWAY isn't another address on $SUBNET"
	gw_from=--gateway
else
	for src in route lease mygate; do
		case $src in
		route) g=$(default_gw) from="the default route" ;;
		lease) g=$(dhcpleasectl -l "$IF" 2>/dev/null | sed -n 's/^[[:space:]]*default gateway //p' | head -n 1) from="the DHCP lease" ;;
		mygate) g=$(sed -n 1p /etc/mygate 2>/dev/null) from=/etc/mygate ;;
		esac
		if on_lan "$g"; then
			GATEWAY=$g gw_from=$from
			break
		fi
	done
	[ -n "$GATEWAY" ] || die "can't tell the LAN's gateway: no default route, DHCP lease or /etc/mygate on $SUBNET. Pass --gateway with the router's address"
fi

# The DHCP pool: the upper half of the subnet, less this box and the gateway.
lo=$((net + (1 << (31 - bits)))) hi=$((bcast - 1)) RANGES=
for x in $( (echo $an; ip2n $GATEWAY) | sort -n); do
	if [ $x -ge $lo ] && [ $x -le $hi ]; then
		[ $x -eq $lo ] || RANGES="$RANGES range $(n2ip $lo) $(n2ip $((x - 1)));"
		lo=$((x + 1))
	fi
done
[ $lo -gt $hi ] || RANGES="$RANGES range $(n2ip $lo) $(n2ip $hi);"
RANGES=${RANGES# }
[ -n "$RANGES" ] || die "no room for a DHCP pool on $SUBNET"
# The reverse zone of the subnet's whole octets, as PocketBase works it out.
REVERSE=in-addr.arpa i=0
for o in $(echo $NETWORK | tr . ' '); do
	[ $i -ge $((bits / 8)) ] || REVERSE=$o.$REVERSE
	i=$((i + 1))
done
echo "interface $IF; subnet $SUBNET (netmask $NETMASK); gateway $GATEWAY, from $gw_from"
echo "DHCP pool: $(echo "$RANGES" | sed 's/range \([^ ]*\) \([^;]*\);/\1-\2/g'); reverse zone $REVERSE"

# Fill in the templates; stop if any placeholder is left.
for f in hostname.if mygate pf.conf unbound.conf static.conf rc.d.pocketbase dnssync dhcpd.conf; do
	sed -e "s|@DOMAIN@|$DOMAIN|g" -e "s|@ADDRESS@|$ADDRESS|g" -e "s|@NETMASK@|$NETMASK|g" \
	    -e "s|@NETWORK@|$NETWORK|g" -e "s|@SUBNET@|$SUBNET|g" -e "s|@BITS@|$BITS|g" \
	    -e "s|@GATEWAY@|$GATEWAY|g" -e "s|@RANGES@|$RANGES|g" $S/$f >$R/$f
	left=$(grep -o '@[A-Z][A-Z_]*@' $R/$f | sort -u | tr '\n' ' ')
	[ -z "$left" ] || die "$f has placeholders this installer doesn't know: $left"
done

step "originals (*.orig, kept for rollback: RUNBOOK §10)"
# Only files that aren't ours yet (ours name HomeSubnetDns, or match what we'd
# install); a box set up earlier already has these.
printf 'nameserver 127.0.0.1\nlookup file bind\n' >$t/resolv.conf
# /etc/sysctl.conf with the ARP timeout the LAN scan needs (D44) at the end;
# any other lines in it are kept.
arp_note="# HomeSubnetDns: keep ARP entries 5 minutes, so the LAN scan sees machines leave (D44)."
arp_name=net.inet.ip.arptimeout arp_value=300
{
	[ ! -e /etc/sysctl.conf ] || grep -vxF -e "$arp_note" /etc/sysctl.conf | grep -v "^$arp_name=" || true
	echo "$arp_note"
	echo "$arp_name=$arp_value"
} >$t/sysctl.conf
for pair in /etc/hostname.$IF:$R/hostname.if /etc/pf.conf:$R/pf.conf $E/unbound.conf:$R/unbound.conf \
    /etc/resolv.conf:$t/resolv.conf /etc/dhcpd.conf:$R/dhcpd.conf /etc/sysctl.conf:$t/sysctl.conf; do
	f=${pair%%:*}
	if [ -e "$f" ] && [ ! -e "$f.orig" ] && ! cmp -s "$f" "${pair#*:}" && ! grep -q HomeSubnetDns "$f"; then
		act cp -p "$f" "$f.orig"
	fi
done

step "network: $IF at $ADDRESS/$BITS, gateway $GATEWAY"
if [ "$cur" = "$ADDRESS" ]; then
	if [ "$(tail -n 1 /var/log/netswitch.log 2>/dev/null)" = "+ sleep 180" ]; then
		# An earlier run switched the address; it reverts unless confirmed here.
		if ping -c 2 -w 2 $GATEWAY >/dev/null 2>&1 && ping -c 2 -w 2 9.9.9.9 >/dev/null 2>&1; then
			act touch /tmp/net-ok
			echo "the gateway and the internet answer: switch to $ADDRESS confirmed"
		else
			die "at $ADDRESS the gateway or the internet doesn't answer; the box goes back to its old settings within 3 minutes"
		fi
	fi
	if put $R/hostname.if /etc/hostname.$IF 640 root:wheel; then echo "(the address is already live; the file is for the next boot)"; fi
	put $R/mygate /etc/mygate 644 root:wheel || true
	g=$(default_gw)
	if [ "$g" = "$GATEWAY" ]; then
		echo "unchanged: default route via $GATEWAY"
	elif [ -n "$g" ]; then
		echo "differs: default route via $g"
		act route -qn change -inet default $GATEWAY
	else
		echo "missing: default route"
		act route -qn add -inet default $GATEWAY
	fi
	others=$(echo $addrs | tr ' ' '\n' | grep -v '^0x' | grep -vx "$ADDRESS" | tr '\n' ' ')
	[ -z "$others" ] || echo "note: $IF also has $others(not in /etc/hostname.$IF, so gone at the next boot)"
	if dup=$(grep "duplicate IP address $ADDRESS" /var/log/messages 2>/dev/null | tail -n 1) && [ -n "$dup" ]; then
		echo "warning: another machine has claimed $ADDRESS, most recently: $dup"
		echo "         If the old box is still up, shut it down (RUNBOOK §5)."
	fi
else
	echo "$IF has $cur, not $ADDRESS: switching (D42)"
	if ping -c 2 -w 2 $ADDRESS >/dev/null 2>&1; then
		die "something already answers on $ADDRESS: shut the old box down, or pick another ADDRESS in site.conf (RUNBOOK §5)"
	fi
	act install -m 700 -o root -g wheel $S/netswitch.sh /root/netswitch.sh
	dir $N 700 root:wheel
	act install -m 600 -o root -g wheel $R/hostname.if $R/mygate $N/
	if [ "$check" = no ]; then
		echo "sh /root/netswitch.sh $IF $N" | at now
		cat <<EOF
$IF switches from $cur to $ADDRESS within a minute. Within 3 minutes of that,
run this again to confirm the switch; without that, the box goes back to its
old settings.
  $(rerun)
An SSH session to $cur drops at the switch. To reconnect, on the workstation:
  ssh-keygen -R $ADDRESS        # forget any earlier key for the address
  ssh -o StrictHostKeyChecking=accept-new ${DOAS_USER:-<user>}@$ADDRESS true
(and with scripts/install-box, leave out RDOAS_HOST from then on: it uses
ADDRESS from site.conf). Once it's done, devices that run hostupdate need
install.sh run again with --box $ADDRESS (RUNBOOK §8a).
EOF
		exit 0
	fi
fi

step "ARP: entries last 5 minutes, so the LAN scan sees machines leave (D44)"
put $t/sysctl.conf /etc/sysctl.conf 644 root:wheel || true
if [ "$(sysctl -n $arp_name)" = $arp_value ]; then
	echo "unchanged: $arp_name is $arp_value"
else
	echo "differs: $arp_name is $(sysctl -n $arp_name)"
	act sysctl $arp_name=$arp_value
fi

step "users"
if id _pocketbase >/dev/null 2>&1; then
	echo "exists: _pocketbase"
else
	act useradd -c "PocketBase" -d $P -s /sbin/nologin -g =uid _pocketbase
fi
if id _dnssync >/dev/null 2>&1; then
	echo "exists: _dnssync"
	id -Gn _dnssync | grep -qw _unbound || act usermod -G _unbound _dnssync
else
	act useradd -c "PocketBase to Unbound sync" -d /var/dnssync -s /sbin/nologin -g =uid -G _unbound _dnssync
fi

step "unbound"
dir $H 770 root:_unbound
ub=no
if put $R/unbound.conf $E/unbound.conf 644 root:wheel; then ub=yes; fi
if put $R/static.conf $H/static.conf 644 root:wheel; then ub=yes; fi
if [ ! -e $H/pocketbase.conf ]; then
	echo "# Generated by dnssync from PocketBase -- do not edit." >$t/pocketbase.conf
	put $t/pocketbase.conf $H/pocketbase.conf 640 _dnssync:_unbound || true
fi
if [ ! -s /var/unbound/db/root.key ]; then
	echo "missing: /var/unbound/db/root.key"
	act unbound-anchor -a /var/unbound/db/root.key || true	# 1 means it wrote the anchor
	act chown _unbound:_unbound /var/unbound/db/root.key
fi
if [ "$check" = no ]; then
	unbound-checkconf >/dev/null || die "unbound-checkconf rejects the config; the old one is in $E/unbound.conf.orig"
fi
enabled unbound || act rcctl enable unbound
if ! running unbound; then
	act rcctl start unbound
elif [ $ub = yes ]; then
	act rcctl restart unbound
elif ! dig @$ADDRESS +time=2 +tries=2 dns.$DOMAIN >/dev/null 2>&1; then
	# e.g. it started before the address existed, or the address moved by hand.
	echo "running, but doesn't answer on $ADDRESS"
	act rcctl restart unbound
fi

step "pf, and the box's own DNS"
pfctl -nf $R/pf.conf || die "pfctl rejects the rendered pf.conf"
if put $R/pf.conf /etc/pf.conf 600 root:wheel; then act pfctl -f /etc/pf.conf; fi
if enabled resolvd; then act rcctl disable resolvd; fi
if running resolvd; then act rcctl stop resolvd; fi
put $t/resolv.conf /etc/resolv.conf 644 root:wheel || true

step "pocketbase"
# A scripts/backup file holds info.txt, pb_data/ and maybe dhcpd.leases, and nothing else.
check_backup() {
	list=$(tar -tzf "$1") || die "can't list $1: not a gzipped tar file, or a damaged one"
	echo "$list" | grep -qx 'pb_data/data.db' || die "$1 has no pb_data/data.db, so it isn't a scripts/backup file"
	bad=$(echo "$list" | grep -Ev '^(info\.txt|dhcpd\.leases|pb_data/?|pb_data/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*/?)$' | head -n 3 | tr '\n' ' ')
	if echo "$list" | grep -Eq '(^|/)\.\.(/|$)'; then bad="$bad .."; fi
	[ -z "$bad" ] || die "$1 has unexpected entries ($bad), so it isn't a scripts/backup file"
}
restore_data() {
	r=$(mktemp -d $P/restore.XXXXXX)
	tar -xzf "$restore" -C "$r"
	if [ -d $P/pb_data ]; then rmdir $P/pb_data; fi	# empty: it has no data.db
	mv "$r/pb_data" $P/pb_data
	chown -R _pocketbase:_pocketbase $P/pb_data
	chmod 700 $P/pb_data
	if [ -f "$r/dhcpd.leases" ] && [ ! -s /var/db/dhcpd.leases ]; then
		install -m 644 -o root -g wheel "$r/dhcpd.leases" /var/db/dhcpd.leases
		echo "restored the DHCP leases"
	fi
	cat "$r/info.txt" 2>/dev/null || true
	rm -rf "$r"
	echo "$sum" >$P/.restored
}
make_login() {
	pw=$(openssl rand -hex 24)
	# Creates the account, or on restored data sets a new password on the one there.
	printf '%s\n' "$pw" | su -s /bin/sh _pocketbase -c "/usr/local/bin/pocketbase service-account $SA --dir $P/pb_data"
	(umask 077; printf 'email=%s\npassword=%s\n' "$SA" "$pw" >/var/dnssync/.pbcreds)
	chown _dnssync:_dnssync /var/dnssync/.pbcreds
	pw=
}
pb_answers() { ftp -M -V -w 3 -o /dev/null http://$ADDRESS:8090/api/health >/dev/null 2>&1; }

dir $P 700 _pocketbase:_pocketbase
fresh=no restored=no
if [ -n "$restore" ]; then
	sum=$(sha256 -q "$restore")
	if [ "$(cat $P/.restored 2>/dev/null)" = "$sum" ]; then
		echo "restored earlier from this backup"
		# In case that run stopped before it set dnssync's login.
		[ /var/dnssync/.pbcreds -nt $P/.restored ] || restored=yes
	elif [ -e $P/pb_data/data.db ]; then
		die "$P/pb_data already has data. To replace it with the backup: rcctl stop pocketbase, move $P/pb_data aside, and run again; to keep it, leave out --restore"
	else
		check_backup "$restore"
		echo "restoring PocketBase's data from the backup"
		act restore_data
		restored=yes
	fi
	# The copy scripts/install-box uploaded; a file given to a bundle is left alone.
	if [ -z "$again" ] && [ "$restore" -ef "$S/restore.tgz" ] && [ "$check" = no ]; then rm -f "$restore"; fi
elif [ ! -e $P/pb_data/data.db ]; then
	fresh=yes
	echo "no data and no --restore: PocketBase starts with an empty database"
fi
dir $P/pb_data 700 _pocketbase:_pocketbase

pb=no
if running pocketbase && ! cmp -s $S/pocketbase /usr/local/bin/pocketbase; then
	# A new build may migrate the data, so keep a copy first.
	act rcctl stop pocketbase
	act rm -rf $P/pb_data.prev
	act cp -Rp $P/pb_data $P/pb_data.prev
fi
if put $S/pocketbase /usr/local/bin/pocketbase 555 root:bin; then pb=yes; fi
if put $R/rc.d.pocketbase /etc/rc.d/pocketbase 555 root:bin; then pb=yes; fi
dir /var/dnssync 700 _dnssync:_dnssync
if [ $restored = yes ]; then
	echo "restored data: setting dnssync's login, with a new random password"
	act make_login
elif [ -s /var/dnssync/.pbcreds ]; then
	echo "exists: /var/dnssync/.pbcreds (dnssync's login)"
else
	echo "missing: /var/dnssync/.pbcreds: setting dnssync's login, with a new random password"
	act make_login
fi
enabled pocketbase || act rcctl enable pocketbase
if ! running pocketbase; then
	act rcctl start pocketbase
elif [ $pb = yes ]; then
	act rcctl restart pocketbase
elif ! pb_answers; then
	echo "running, but doesn't answer on $ADDRESS:8090"
	act rcctl restart pocketbase
fi
if [ "$check" = no ]; then
	n=0
	until pb_answers; do
		n=$((n + 1))
		[ $n -lt 15 ] || die "PocketBase doesn't answer on $ADDRESS:8090 (see /var/log/daemon)"
		sleep 1
	done
	echo "PocketBase answers on $ADDRESS:8090"
fi

step "dnssync"
put $R/dnssync /usr/local/libexec/dnssync 555 root:bin || true
if [ -e $H/pocketbase.conf ] && [ "$(stat -f '%Lp %Su:%Sg' $H/pocketbase.conf)" != "640 _dnssync:_unbound" ]; then
	act chown _dnssync:_unbound $H/pocketbase.conf
	act chmod 640 $H/pocketbase.conf
fi
if [ "$(crontab -u _dnssync -l 2>/dev/null | grep -v '^#' || true)" = "$(grep -v '^#' $S/dnssync.crontab)" ]; then
	echo "unchanged: _dnssync's crontab"
else
	echo "differs: _dnssync's crontab"
	act crontab -u _dnssync $S/dnssync.crontab
fi

step "hostname: dns.$DOMAIN"
if [ "$(cat /etc/myname 2>/dev/null)" = "dns.$DOMAIN" ] && [ "$(hostname)" = "dns.$DOMAIN" ]; then
	echo "unchanged"
else
	echo "dns.$DOMAIN" >$t/myname
	put $t/myname /etc/myname 644 root:wheel || true
	act hostname "dns.$DOMAIN"
	act rcctl restart syslogd
fi

step "dhcpd"
dhcpd -n -c $R/dhcpd.conf || die "dhcpd rejects the rendered dhcpd.conf"
dh=no dhcp_todo=no
if put $R/dhcpd.conf /etc/dhcpd.conf 644 root:wheel; then dh=yes; fi
if running dhcpd; then
	if [ "$(rcctl get dhcpd flags)" != "$IF" ]; then
		act rcctl set dhcpd flags "$IF"
		dh=yes
	fi
	if [ $dh = yes ]; then act rcctl restart dhcpd; fi
	echo "running on $IF"
elif [ $start_dhcpd = yes ]; then
	[ -e /var/db/dhcpd.leases ] || act touch /var/db/dhcpd.leases
	act rcctl enable dhcpd
	act rcctl set dhcpd flags "$IF"
	act rcctl start dhcpd
else
	dhcp_todo=yes
	echo "installed, not started: that needs --start-dhcpd (see the end)"
fi

step "OS patches (last, so a reboot they need doesn't hold up the rest)"
reboot_todo=no
if ! avail=$(syspatch -c 2>&1); then
	echo "warning: can't check for patches, so skipping them; run this again later. syspatch: $avail"
elif [ -z "$avail" ]; then
	echo "up to date"
else
	echo "available:" $avail
	if [ "$check" = yes ]; then
		act syspatch
	else
		pending=$((pending + 1))
		out=$(syspatch 2>&1) || die "syspatch failed: $out"
		echo "$out"
		case $out in *reboot*) reboot_todo=yes ;; esac
	fi
fi
# A kernel patch installs a kernel with a new version string; until a reboot
# loads it, it differs from the running one. This also catches a patch that an
# earlier run, or syspatch by hand, installed.
running_kernel=$(sysctl -n kern.version | head -n 1)
new_kernel=$(strings /bsd 2>/dev/null | grep -m 1 '^OpenBSD [0-9]' || true)
if [ -n "$new_kernel" ] && [ "$new_kernel" != "$running_kernel" ]; then
	echo "a new kernel waits for a reboot: /bsd is \"$new_kernel\", running \"$running_kernel\""
	reboot_todo=yes
fi

step "checks (these run dnssync and the LAN scan once, as they run every minute or two)"
fails=0
ok() { echo "ok:   $*"; }
bad() { echo "FAIL: $*"; fails=$((fails + 1)); }
if [ "$check" = yes ] && [ $pending -gt 0 ]; then
	echo "(--check: these show the box as it is now, before the changes above)"
fi
for s in unbound pocketbase cron; do
	if running $s; then ok "$s running"; else bad "$s isn't running"; fi
done
if running dhcpd; then
	ok "dhcpd running"
elif [ $dhcp_todo = yes ]; then
	echo "todo: dhcpd (see the end)"
else
	bad "dhcpd isn't running"
fi
if [ "$(default_gw)" = "$GATEWAY" ]; then ok "default route via $GATEWAY"; else bad "no default route via $GATEWAY"; fi
if su -s /bin/sh _dnssync -c /usr/local/libexec/dnssync 2>/dev/null; then
	ok "dnssync: $(grep -c '^local-data:' $H/pocketbase.conf) names and $(grep -c '^local-data-ptr:' $H/pocketbase.conf) reverse records from PocketBase"
else
	bad "dnssync failed (see /var/log/daemon)"
fi
a=$(dig @127.0.0.1 +short +time=3 +tries=2 openbsd.org A 2>/dev/null || true)
if [ -n "$a" ]; then ok "lookups work (openbsd.org)"; else bad "openbsd.org doesn't resolve"; fi
if dig @127.0.0.1 +dnssec +time=5 isc.org SOA 2>/dev/null | grep -q '^;; flags:.* ad[ ;]'; then
	ok "DNSSEC: isc.org validates"
else
	bad "DNSSEC: isc.org doesn't validate (no ad flag)"
fi
# A domain signed wrongly on purpose. Its servers can be slow to answer at all;
# only an answer that isn't SERVFAIL means a bad signature got through.
case $(dig @127.0.0.1 +time=5 +tries=2 dnssec-failed.org 2>/dev/null | sed -n 's/.*status: \([A-Z]*\).*/\1/p') in
SERVFAIL) ok "DNSSEC: dnssec-failed.org is refused" ;;
'') echo "note: dnssec-failed.org didn't answer in time, so that check was skipped" ;;
*) bad "DNSSEC: dnssec-failed.org's bad signature was accepted" ;;
esac
if [ "$(dig @$ADDRESS +short +time=3 dns.$DOMAIN 2>/dev/null || true)" = "$ADDRESS" ]; then
	ok "dns.$DOMAIN -> $ADDRESS, asked on the LAN address"
else
	bad "dns.$DOMAIN isn't served on $ADDRESS"
fi
if [ "$(dig @$ADDRESS +short +time=3 -x $ADDRESS 2>/dev/null || true)" = "dns.$DOMAIN." ]; then
	ok "$ADDRESS -> dns.$DOMAIN"
else
	bad "no reverse name for $ADDRESS"
fi
case $(ftp -M -V -w 5 -o - http://$ADDRESS:8090/api/hostupdate/info 2>/dev/null | tr '\n' ' ' || true) in
"domain=$DOMAIN reverse=$REVERSE ") ok "device installers get domain=$DOMAIN, reverse=$REVERSE" ;;
*) bad "/api/hostupdate/info doesn't give domain=$DOMAIN and reverse=$REVERSE" ;;
esac
case $(ftp -M -V -w 5 -o /dev/null http://$ADDRESS:8090/api/hostupdate 2>&1 || true) in
*401*) ok "check-ins without a login get 401" ;;
*) bad "the check-in endpoint doesn't answer 401 without a login" ;;
esac
# The scan PocketBase runs every 2 minutes (D44), saved like those; at least
# the gateway must answer it.
if [ "$check" = yes ] && [ $pb = yes ]; then
	echo "note: the LAN scan check needs the new PocketBase, so it runs once that's installed"
elif scan=$(su -s /bin/sh _pocketbase -c "/usr/local/bin/pocketbase lan-scan --dir=$P/pb_data --lan=$ADDRESS/$BITS --gateway=$GATEWAY" 2>&1); then
	if echo "$scan" | awk -v g="$GATEWAY" '$1 == g { f = 1 } END { exit !f }'; then
		ok "LAN scan: $(echo "$scan" | tail -n 1)"
	else
		bad "LAN scan: the gateway $GATEWAY didn't answer"
	fi
else
	bad "LAN scan failed: $(echo "$scan" | tail -n 1)"
fi

step "summary"
if [ "$check" = yes ]; then
	if [ $pending -eq 0 ]; then echo "nothing to change"; else echo "$pending change(s) above would be made"; fi
fi
if [ $fresh = yes ] && [ "$check" = no ]; then
	cat <<EOF
PocketBase started with an empty database (no --restore):
- Within 30 minutes, create the admin account in a browser, from the link
  this prints (if it has expired: rcctl restart pocketbase):
    grep -o "http://[^ ]*pbinstall/[^ ]*" /var/log/daemon | tail -1
  (from the workstation: ssh ${DOAS_USER:-<user>}@$ADDRESS 'grep ...' with the same pattern).
- Add the hand-made hosts records again, and run install.sh --box $ADDRESS on
  each device: it prints the name and password to enter under devices
  (RUNBOOK §8a).
EOF
fi
if [ $dhcp_todo = yes ]; then
	cat <<EOF
dhcpd is installed but not started: two DHCP servers on one network hand out
clashing addresses. Make sure the router's DHCP server is off (RUNBOOK §6) and
the old box is down, then run this again with --start-dhcpd:
  $(rerun --start-dhcpd)
EOF
fi
if [ $reboot_todo = yes ]; then
	echo "A kernel patch needs a reboot (DNS stops for about a minute): run shutdown -r now (from the workstation: timeout 60 scripts/rdoas jobs/03-reboot.sh), then run this again: $(rerun)"
fi
if [ "$check" = yes ]; then
	[ $fails -eq 0 ] || [ $pending -eq 0 ] || { echo "$fails check(s) failed; the changes above may fix them"; exit 0; }
fi
[ $fails -eq 0 ] || die "$fails check(s) failed, see above"
[ "$check" = no ] || exit 0
if [ $reboot_todo = yes ]; then
	:
elif [ $dhcp_todo = no ]; then
	echo "done: this box serves DNS and DHCP for the LAN"
else
	echo "done, except dhcpd: this box serves DNS for the LAN"
fi
