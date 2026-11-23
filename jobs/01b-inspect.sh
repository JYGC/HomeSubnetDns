echo "--- doas.conf"; cat /etc/doas.conf
echo "--- pf.conf"; cat /etc/pf.conf
echo "--- pfctl -sr"; pfctl -sr
echo "--- syspatch -c"; syspatch -c; echo "syspatch rc=$?"
echo "--- originals"; ls -l /etc/*.orig /var/unbound/etc/*.orig
