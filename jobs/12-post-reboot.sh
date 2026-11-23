sleep 60	# let at least one cron minute pass after boot
uptime
for s in unbound pocketbase cron resolvd dhcpd; do rcctl check $s; done
ifconfig egress | grep "inet "; route -n show -inet | grep default
cat /etc/resolv.conf
pfctl -sr | grep -E "53|8090"
echo "--- dnssync cron runs since boot"
grep dnssync /var/cron/log | tail -2
echo "--- dnssync manual run (expect rc=0, no change)"
su -s /bin/sh _dnssync -c /usr/local/libexec/dnssync; echo "rc=$?"
grep -E "dnssync|pocketbase|unbound" /var/log/daemon | grep -v pbinstall | tail -4
