DOMAIN=$(sed 's/^dns\.//' /etc/myname)
echo "=== 1. service account must be read-only (expect HTTP 400/403 on create)"
su -s /bin/sh _dnssync -c 'perl -MHTTP::Tiny -MJSON::PP -e '\''
	my %c = map { /^(\w+)=(.*)$/ ? ($1,$2) : () } split /\n/, do { local(@ARGV,$/)="/var/dnssync/.pbcreds"; <> };
	my $h = HTTP::Tiny->new; my $j = JSON::PP->new;
	my ($api) = do { local(@ARGV,$/)="/usr/local/libexec/dnssync"; <> } =~ /^my \$API\s*=\s*.(http[^\x27]+)/m;  # as dnssync has it
	my $r = $h->post("$api/collections/service_accounts/auth-with-password",
	    { headers => {"Content-Type"=>"application/json"}, content => $j->encode({identity=>$c{email}, password=>$c{password}}) });
	my $t = $j->decode($r->{content})->{token};
	$r = $h->post("$api/collections/hosts/records",
	    { headers => {"Content-Type"=>"application/json", Authorization=>$t}, content => $j->encode({name=>"evil", ipv4=>"1.2.3.4"}) });
	print "create as service account: HTTP $r->{status}\n";
	$r = $h->get("$api/collections/hosts/records");
	print "list without auth: HTTP $r->{status}, items=", scalar @{ $j->decode($r->{content})->{items} // [] }, "\n";
'\'''

echo "=== 2. PocketBase down: DNS must keep working, dnssync must not touch the file"
before=$(sha256 -q /var/unbound/etc/home.d/pocketbase.conf)
rcctl stop pocketbase
su -s /bin/sh _dnssync -c /usr/local/libexec/dnssync; echo "dnssync rc=$? (expect 1)"
after=$(sha256 -q /var/unbound/etc/home.d/pocketbase.conf)
[ "$before" = "$after" ] && echo "pocketbase.conf unchanged: OK" || echo "pocketbase.conf CHANGED: FAIL"
dig @127.0.0.1 test1.$DOMAIN +short
grep dnssync /var/log/daemon | tail -1
rcctl start pocketbase
sleep 3
su -s /bin/sh _dnssync -c /usr/local/libexec/dnssync; echo "dnssync after restart rc=$? (expect 0, no change)"
