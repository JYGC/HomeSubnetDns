# Back up PocketBase's data (pb_data, except its own backups/) and the DHCP
# leases to ~/.rdoas/backup.tgz of the user running doas, for scripts/backup
# to fetch.
# PocketBase stops while its files are copied, so the copy is consistent; DNS
# keeps working from the generated file meanwhile. A PocketBase that isn't
# running, or doesn't start again, doesn't stop the backup.
set -e
umask 077
P=/var/pocketbase/pb_data
: "${DOAS_USER:?run it through doas: scripts/backup}"
OUT=$(cd "$(dirname "$0")" && pwd)/backup.tgz	# ~/.rdoas, where scripts/rdoas put this job
t=$(mktemp -d)
was=stopped
if rcctl check pocketbase >/dev/null; then was=running; fi
trap 'rm -rf "$t"; [ $was = stopped ] || rcctl check pocketbase >/dev/null || rcctl start pocketbase' EXIT

mkdir $t/pb_data
[ $was = stopped ] || rcctl stop pocketbase
for f in $(ls -A $P); do
	[ "$f" = backups ] || cp -Rp "$P/$f" $t/pb_data/
done
if [ $was = running ] && ! rcctl start pocketbase; then
	echo "warning: PocketBase didn't start again (see /var/log/daemon); backing up anyway"
fi
if [ -f /var/db/dhcpd.leases ]; then cp -p /var/db/dhcpd.leases $t/; else echo "(no DHCP leases)"; fi
printf 'HomeSubnetDns backup of %s, %s\n' "$(hostname)" "$(date -u '+%Y-%m-%d %H:%M UTC')" >$t/info.txt
(cd $t && tar -czf $OUT info.txt pb_data $(ls dhcpd.leases 2>/dev/null))
chown "$DOAS_USER" $OUT
echo "PocketBase was $was: $(rcctl check pocketbase || true)"
ls -l $OUT
tar -tzf $OUT
