# DNS traffic between the box and the router (the gateway in /etc/mygate), 20 s.
IF=$(ifconfig egress | sed -n '1s/:.*//p')
tcpdump -n -l -i $IF "port 53 and host $(sed -n 1p /etc/mygate)" 2>&1 &
pid=$!
sleep 20
kill $pid
