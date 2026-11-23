# Read-only: the PocketBase hosts records as dnssync sees them, the generated
# file, and what Unbound serves.
DOMAIN=$(sed 's/^dns\.//' /etc/myname)
echo "--- PocketBase hosts (name, ipv4, alias_of with +ptr if it takes the PTR, device, disabled)"
su -s /bin/sh _dnssync -c 'perl -MHTTP::Tiny -MJSON::PP -e '\''
	my %c = map { /^(\w+)=(.*)$/ ? ($1,$2) : () } split /\n/, do { local(@ARGV,$/)="/var/dnssync/.pbcreds"; <> };
	my $h = HTTP::Tiny->new; my $j = JSON::PP->new;
	my ($api) = do { local(@ARGV,$/)="/usr/local/libexec/dnssync"; <> } =~ /^my \$API\s*=\s*.(http[^\x27]+)/m;  # as dnssync has it
	my $r = $h->post("$api/collections/service_accounts/auth-with-password",
	    { headers => {"Content-Type"=>"application/json"}, content => $j->encode({identity=>$c{email}, password=>$c{password}}) });
	my $t = $j->decode($r->{content})->{token};
	$r = $h->get("$api/collections/hosts/records?perPage=500&sort=name&expand=alias_of",
	    { headers => {Authorization=>$t} });
	my $items = $j->decode($r->{content})->{items} // [];
	die "no alias_of field: the aliases migration has not run\n" if @$items && !exists $items->[0]{alias_of};
	printf "%-28s %-15s %-32s %-6s %s\n", $_->{name}, $_->{ipv4} || "-",
	    $_->{alias_of} ? "-> " . ($_->{expand}{alias_of}{name} // "?") . ($_->{takes_ptr} ? " +ptr" : "") : "-",
	    $_->{device} ? "device" : "-", $_->{disabled} ? "disabled" : "" for @$items;
'\'''
echo "--- generated pocketbase.conf"
cat /var/unbound/etc/home.d/pocketbase.conf
echo "--- served by unbound (besides $DOMAIN itself, reverse zones and built-in ones)"
unbound-control list_local_data | grep -Ev "^$DOMAIN|\.arpa\.|^(localhost|invalid|onion|test)\.|^[[:space:]]*\$" | sort
