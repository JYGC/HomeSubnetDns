# Read-only diagnosis for "names don't resolve on a client": the box's own
# health, the router's IPv6 RA (and its RDNSS option, the D4 bypass), and
# whether a client's DNS queries reach the box.
# usage: scripts/rdoas jobs/21-diagnose-clients.sh <client IPv4 address>
CLIENT=${1-}
[ -n "$CLIENT" ] || { echo "usage: scripts/rdoas jobs/21-diagnose-clients.sh <client address>" >&2; exit 2; }
DOMAIN=$(sed 's/^dns\.//' /etc/myname)
IF=$(ifconfig egress | sed -n '1s/:.*//p')

echo "--- services"
rcctl check unbound pocketbase dhcpd
echo "--- unbound: answers and counters"
dig @127.0.0.1 dns.$DOMAIN +short
unbound-control stats_noreset | grep -E '^total\.num\.(queries|cachehits)|^num\.answer\.rcode\.(NOERROR|NXDOMAIN|SERVFAIL)='
echo "--- recent unbound/dnssync errors"
grep -E 'unbound|dnssync' /var/log/daemon | grep -viE 'updated|info: ' | tail -5

echo "--- router advertisements (soliciting one; 20 s)"
tcpdump -n -l -v -i $IF 'icmp6 and ip6[40] == 134' 2>/dev/null &
pid=$!
sleep 2
slaacctl send solicitation $IF
sleep 18
kill $pid

echo "--- DNS queries from $CLIENT to this box (40 s)"
tcpdump -n -l -i $IF "src host $CLIENT and dst port 53" 2>/dev/null &
pid=$!
sleep 40
kill $pid
echo "--- done"
