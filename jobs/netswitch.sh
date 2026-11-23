#!/bin/sh
# Move the LAN interface to the settings in DIR/hostname.if and DIR/mygate,
# which the installer renders (D42). Run detached via at(1): the SSH session
# that started it may be on the old address. Unless /tmp/net-ok appears within
# 180 s, it puts the previous settings back, whatever they were (DHCP, or
# another static address).
# usage: sh netswitch.sh IF DIR
IF=$1
F=$2
B=/root/netswitch.prev	# the settings from before the switch
exec >/var/log/netswitch.log 2>&1
set -x
date
rm -f /tmp/net-ok

# Configure $IF from /etc/hostname.$IF and /etc/mygate alone: first drop its
# DHCP client, its IPv4 addresses and the default route.
reconfigure() {
	ifconfig $IF inet -autoconf
	sleep 3
	for a in $(ifconfig $IF | sed -n 's/^[[:space:]]*inet \([0-9.]*\) .*/\1/p'); do
		ifconfig $IF inet $a delete
	done
	route -n delete -inet default
	sh /etc/netstart $IF
	if [ -s /etc/mygate ] && ! route -n show -inet | grep -q '^default'; then
		route -n add -inet default "$(sed -n 1p /etc/mygate)"
	fi
	ifconfig $IF
	route -n show -inet
}

rm -rf $B
mkdir -m 700 $B
[ ! -e /etc/hostname.$IF ] || cp -p /etc/hostname.$IF $B/hostname.if
[ ! -e /etc/mygate ] || cp -p /etc/mygate $B/mygate
install -m 640 -o root -g wheel $F/hostname.if /etc/hostname.$IF
install -m 644 -o root -g wheel $F/mygate /etc/mygate
reconfigure

sleep 180
if [ -e /tmp/net-ok ]; then
	echo "confirmed; keeping the new settings"
	exit 0
fi

echo "NOT confirmed; putting the previous settings back"
if [ -e $B/hostname.if ]; then cp -p $B/hostname.if /etc/hostname.$IF; else rm -f /etc/hostname.$IF; fi
if [ -e $B/mygate ]; then cp -p $B/mygate /etc/mygate; else rm -f /etc/mygate; fi
reconfigure
logger -t netswitch "the new address wasn't confirmed within 3 minutes: put the previous settings back"
