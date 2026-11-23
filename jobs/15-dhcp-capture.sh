tcpdump -n -l -v -i $(ifconfig egress | sed -n '1s/:.*//p') 'udp port 67 or udp port 68' 2>&1 &
pid=$!
sleep 240
kill $pid
echo "--- leases"; cat /var/db/dhcpd.leases
echo "--- dhcpd log"; grep dhcpd /var/log/daemon | tail -10
