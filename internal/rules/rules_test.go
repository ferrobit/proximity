package rules

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func parseOne(t *testing.T, directive string, line string) Rule {
	result, err := Parse(directive, []string{line})
	require.NoError(t, err)
	require.Len(t, result, 1)
	return result[0]
}

func TestCheck_Domains(t *testing.T) {
	items := []Item{
		{Key: "a", Label: "#1", Target: "A",
			Rules: []Rule{{Directive: DomainSuffix, Value: "corp.test"}}},
		{Key: "b", Label: "#2", Target: "B", Rules: []Rule{{Directive: Domain, Value: "wiki.corp.test"}}},
		{Key: "c", Label: "#3", Target: "A", Rules: []Rule{{Directive: DomainKeyword, Value: "wiki"}}},
		{Key: "d", Label: "#4", Target: "A", Rules: []Rule{{Directive: Domain, Value: "other.test"}}},
		{Key: "e", Label: "#5", Target: "A", Rules: []Rule{{Directive: Domain, Value: "wiki.other"}}},
	}
	result := Check(items)
	require.Len(t, result, 2)
	require.Equal(t, []string{"a", "b"}, result[0].Keys)
	require.Contains(t, result[0].Message, "DOMAIN-SUFFIX 'corp.test' in #1 covers DOMAIN")
	require.Contains(t, result[0].Message, "different targets: 'A' and 'B'")
	require.Equal(t, []string{"c", "e"}, result[1].Keys)
	require.Equal(t, "DOMAIN-KEYWORD 'wiki' in #3 covers DOMAIN 'wiki.other' in #5",
		result[1].Message)
}

func TestCheck_DomainsOrder(t *testing.T) {
	narrow := Item{Key: "a", Label: "'Filter 1'", Target: "DIRECT",
		Rules: []Rule{{Directive: DomainSuffix, Value: "im-squadus.elvees.com"}}}
	wide := Item{Key: "b", Label: "'Filter 2'", Target: "OpenVPN 1",
		Rules: []Rule{{Directive: DomainSuffix, Value: "elvees.com"}}}
	require.Empty(t, Check([]Item{narrow, wide}))
	{
		result := Check([]Item{wide, narrow})
		require.Len(t, result, 1)
		require.Equal(t, "DOMAIN-SUFFIX 'elvees.com' in 'Filter 2' covers DOMAIN-SUFFIX "+
			"'im-squadus.elvees.com' in 'Filter 1' (different targets: 'OpenVPN 1' and 'DIRECT')",
			result[0].Message)
		require.Equal(t, []string{"b", "a"}, result[0].Keys)
	}
	{
		exact := Item{Key: "c", Label: "#3", Target: "DIRECT",
			Rules: []Rule{{Directive: Domain, Value: "elvees.com"}}}
		require.Empty(t, Check([]Item{exact, wide}))
		require.Len(t, Check([]Item{wide, exact}), 1)
	}
}

func TestCheck_Duplicates(t *testing.T) {
	{
		rule := Rule{Directive: Domain, Value: "example.com"}
		result := Check([]Item{{Key: "a", Label: "#1", Target: "A", Rules: []Rule{rule, rule}}})
		require.Len(t, result, 1)
		require.Equal(t, "duplicate DOMAIN 'example.com' in #1 and #1", result[0].Message)
	}
	{
		items := []Item{}
		for range 20 {
			items = append(items, Item{Key: "a", Label: "#1", Rules: []Rule{{Directive: GeoIP,
				Value: "RU"}}})
		}
		require.Len(t, Check(items), maxWarnings)
	}
	{
		require.Empty(t, Check(nil))
	}
}

func TestCheck_Networks(t *testing.T) {
	first := parseOne(t, IPCIDR, "10.0.0.0/8")
	second := parseOne(t, IPCIDR, "10.1.2.3:443")
	third := parseOne(t, IPCIDR, "10.1.2.3:80")
	fourth := parseOne(t, IPCIDR, "192.168.0.0/16")
	{
		result := Check([]Item{{Key: "a", Label: "#1", Target: "A", Rules: []Rule{first}},
			{Key: "b", Label: "#2", Target: "A", Rules: []Rule{second}}})
		require.Len(t, result, 1)
		require.Equal(t, "network '10.0.0.0/8' in #1 covers '10.1.2.3/32:443' in #2",
			result[0].Message)
	}
	{
		require.Empty(t, Check([]Item{{Key: "b", Label: "#1", Target: "A", Rules: []Rule{second}},
			{Key: "a", Label: "#2", Target: "B", Rules: []Rule{first}}}))
		require.Empty(t, Check([]Item{{Key: "a", Label: "#1", Rules: []Rule{second, third}}}))
		host := parseOne(t, IPCIDR, "10.1.2.3")
		require.Len(t, Check([]Item{{Key: "a", Label: "#1", Rules: []Rule{host, second}}}), 1)
		require.Empty(t, Check([]Item{{Key: "a", Label: "#1", Rules: []Rule{second, host}}}))
	}
	{
		result := Check([]Item{{Key: "a", Label: "#1", Rules: []Rule{second, third, fourth}}})
		require.Empty(t, result)
	}
}

func TestIsDirective_Names(t *testing.T) {
	require.True(t, IsDirective("GEOSITE"))
	require.False(t, IsDirective("IP-CIDR6"))
	require.False(t, IsDirective(""))
}

func TestParse_Domain(t *testing.T) {
	{
		require.Equal(t, "www.example.com", parseOne(t, Domain, " WWW.Example.com. ").Value)
		require.Equal(t, "example.com", parseOne(t, Domain, "https://example.com:8443/a?b#c").Value)
		require.Equal(t, "example.com", parseOne(t, DomainSuffix, "*.example.com").Value)
		require.Equal(t, "example.com", parseOne(t, DomainSuffix, "+.example.com").Value)
		require.Equal(t, "my_host", parseOne(t, Domain, "my_host").Value)
	}
	{
		require.Equal(t, "google", parseOne(t, DomainKeyword, "Google").Value)
		require.Equal(t, "*.cdn.example.com", parseOne(t, DomainWildcard, "*.CDN.example.com").Value)
		require.Equal(t, `^ads\d+\.example\.com$`, parseOne(t, DomainRegex,
			`^ads\d+\.example\.com$`).Value)
	}
}

func TestParse_Errors(t *testing.T) {
	for _, item := range [][2]string{{Domain, "bad domain"}, {Domain, "exa(mple.com"},
		{Domain, "-bad.example.com"}, {Domain, "."}, {DomainSuffix, "a,b"}, {DomainKeyword, "a b"},
		{DomainWildcard, "a/b"}, {GeoSite, "you tube"}, {GeoIP, "R U"}, {IPCIDR, "1.2.3"},
		{IPCIDR, "1.2.3.4/33"}, {IPCIDR, "1.2.3.4:0"}, {IPCIDR, "1.2.3.4:70000"},
		{IPCIDR, "1.2.3.4:http"}, {IPCIDR, "[::1]:99999"}, {SourceIPCIDR, "1.2.3.4:80"},
		{IPASN, "cloudflare"}, {TargetPort, "0"}, {TargetPort, "90-80"}, {TargetPort, "80-x"},
		{SourcePort, "x"}, {Network, "icmp"}, {ProcessName, "a,b"}} {
		_, err := Parse(item[0], []string{item[1]})
		require.Error(t, err, item[1])
	}
	{
		_, err := Parse("UNKNOWN", []string{"x"})
		require.EqualError(t, err, "unexpected directive 'UNKNOWN'")
	}
	{
		_, err := Parse(Domain, []string{"", "  "})
		require.EqualError(t, err, "rule parameters do not exist")
	}
	{
		_, err := Parse(Domain, []string{"good.test", "bad test"})
		require.ErrorContains(t, err, "failed to parse line 2 'bad test'")
	}
}

func TestParse_Geo(t *testing.T) {
	require.Equal(t, "youtube", parseOne(t, GeoSite, "YouTube").Value)
	require.Equal(t, "category-ads-all", parseOne(t, GeoSite, "category-ads-all").Value)
	require.Equal(t, "RU", parseOne(t, GeoIP, "ru").Value)
	require.Equal(t, "private", parseOne(t, GeoIP, "Private").Value)
	require.True(t, parseOne(t, GeoIP, "cn, no-resolve").NoResolve)
	require.Equal(t, "13335", parseOne(t, IPASN, "AS13335").Value)
}

func TestParse_Network(t *testing.T) {
	{
		rule := parseOne(t, IPCIDR, "1.2.3.4")
		require.Equal(t, Rule{Directive: IPCIDR, Value: "1.2.3.4/32"}, rule)
		require.Equal(t, []string{"IP-CIDR,1.2.3.4/32,T"}, rule.Lines("T", nil))
	}
	{
		rule := parseOne(t, IPCIDR, "1.2.3.4:443")
		require.Equal(t, Rule{Directive: IPCIDR, Value: "1.2.3.4/32", Port: 443}, rule)
		require.Equal(t, []string{"AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,443)),T"},
			rule.Lines("T", nil))
	}
	{
		rule := parseOne(t, IPCIDR, "10.1.2.3/8,no-resolve")
		require.Equal(t, Rule{Directive: IPCIDR, Value: "10.0.0.0/8", NoResolve: true}, rule)
		require.Equal(t, []string{"IP-CIDR,10.0.0.0/8,T,no-resolve"}, rule.Lines("T", nil))
	}
	{
		rule := parseOne(t, IPCIDR, "10.0.0.0/24:8080 , No-Resolve")
		require.Equal(t, []string{"AND,((IP-CIDR,10.0.0.0/24,no-resolve),(DST-PORT,8080)),T"},
			rule.Lines("T", nil))
	}
	{
		require.Equal(t, Rule{Directive: IPCIDR6, Value: "2001:db8::/32"},
			parseOne(t, IPCIDR, "2001:db8::1/32"))
		require.Equal(t, Rule{Directive: IPCIDR6, Value: "::1/128"}, parseOne(t, IPCIDR, "::1"))
		require.Equal(t, Rule{Directive: IPCIDR6, Value: "::1/128", Port: 53},
			parseOne(t, IPCIDR, "[::1]:53"))
		require.Equal(t, Rule{Directive: IPCIDR6, Value: "2001:db8::/64", Port: 53},
			parseOne(t, IPCIDR, "[2001:db8::5]/64:53"))
		require.Equal(t, Rule{Directive: SourceIPCIDR, Value: "192.168.1.0/24"},
			parseOne(t, SourceIPCIDR, "192.168.1.7/24"))
	}
}

func TestParse_Ports(t *testing.T) {
	require.Equal(t, "443", parseOne(t, TargetPort, " 443 ").Value)
	require.Equal(t, "8000-8100", parseOne(t, TargetPort, "8000 - 8100").Value)
	require.Equal(t, "1024-65535", parseOne(t, SourcePort, "1024-65535").Value)
	require.Equal(t, "UDP", parseOne(t, Network, "udp").Value)
	require.Equal(t, "curl", parseOne(t, ProcessName, "curl").Value)
	require.Equal(t, []string{"DST-PORT,443,T"}, parseOne(t, TargetPort, "443").Lines("T",
		[]Variant{{Port: 443, Target: "T~https"}}))
}

func TestParse_Punycode(t *testing.T) {
	require.Equal(t, "xn--e1afmkfd.xn--p1ai", parseOne(t, Domain, "пример.рф").Value)
	require.Equal(t, "xn--bcher-kva.example", parseOne(t, DomainSuffix, "Bücher.example").Value)
	require.Equal(t, "xn--mnchen-3ya.de", parseOne(t, Domain, "münchen.de").Value)
	require.Equal(t, "xn--80aswg.xn--p1ai", parseOne(t, Domain, "сайт.рф").Value)
	require.Equal(t, "xn--fsq", punycode("例"))
}

func TestRuleLines_Variants(t *testing.T) {
	variants := []Variant{{Port: 443, Target: "P~https"}, {Port: 21, Target: "P~ftp"}}
	{
		rule := parseOne(t, DomainSuffix, "example.com")
		require.Equal(t, []string{"AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,443)),P~https",
			"AND,((DOMAIN-SUFFIX,example.com),(DST-PORT,21)),P~ftp",
			"DOMAIN-SUFFIX,example.com,P"}, rule.Lines("P", variants))
	}
	{
		rule := parseOne(t, IPCIDR, "1.2.3.4:443")
		require.Equal(t, []string{"AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,443)),P~https"},
			rule.Lines("P", variants))
	}
	{
		rule := parseOne(t, IPCIDR, "1.2.3.4:8080")
		require.Equal(t, []string{"AND,((IP-CIDR,1.2.3.4/32),(DST-PORT,8080)),P"},
			rule.Lines("P", variants))
	}
}

func TestRuleText_RoundTrip(t *testing.T) {
	for _, line := range []string{"1.2.3.4/32", "1.2.3.4/32:443", "10.0.0.0/8,no-resolve",
		"[2001:db8::]/64:53", "2001:db8::/32", "10.0.0.0/24:80,no-resolve"} {
		rule := parseOne(t, IPCIDR, line)
		require.Equal(t, line, rule.Text())
		require.Equal(t, rule, parseOne(t, IPCIDR, rule.Text()))
	}
}
