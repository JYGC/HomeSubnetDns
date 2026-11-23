# The machines online on the LAN, wired or Wi-Fi: scans once, as PocketBase
# does every 2 minutes for the online_machines collection (PLAN.md D44), and
# saves the scan the same way. It takes the LAN from PocketBase's flags.
# usage: scripts/rdoas jobs/25-lan-scan.sh
set -eu
args=
for f in $(sed -n 's/^daemon_flags="\(.*\)"$/\1/p' /etc/rc.d/pocketbase); do
	case $f in --dir=* | --lan=* | --gateway=*) args="$args $f" ;; esac
done
case $args in
*--lan=*--gateway=*) ;;
*) echo "/etc/rc.d/pocketbase has no --lan and --gateway: run the installer (RUNBOOK §3)" >&2; exit 1 ;;
esac
su -s /bin/sh _pocketbase -c "/usr/local/bin/pocketbase lan-scan$args"
