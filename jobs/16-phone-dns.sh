# DNS queries from one client to the box, 30 s.
# usage: scripts/rdoas jobs/16-phone-dns.sh <client IPv4 address>
[ -n "${1-}" ] || { echo "usage: scripts/rdoas jobs/16-phone-dns.sh <client address>" >&2; exit 2; }
IF=$(ifconfig egress | sed -n '1s/:.*//p')
tcpdump -n -l -i $IF "host $1 and port 53" 2>/dev/null &
pid=$!
sleep 30
kill $pid
