package router

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/rules"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = vpn.example.com:51820\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\n"

func makeSnapshot(t *testing.T) config.Config {
	result := config.Default()
	for _, outbound := range []config.Outbound{{Type: config.TypeHost, Enabled: true},
		{Type: config.TypeHost, Enabled: true, Host: &config.HostOptions{
			Mode: config.HostInterface, Interface: "eth1"}},
		{Type: config.TypeHost, Enabled: true, Host: &config.HostOptions{
			Mode: config.HostInterface, Interface: "eth2", Gateway: "192.168.2.1"}},
		{Type: config.TypeSocks5, Enabled: true, Socks5: &config.Socks5Options{Server: "10.0.0.1",
			Port: 1080, Username: "user", Password: "pass", TLS: true, SkipCertVerify: true,
			UDP: true}},
		{Type: config.TypeHTTP, Enabled: true, HTTP: &config.HTTPOptions{Server: "proxy.test",
			Port: 3128, HTTPSServer: "secure.test", HTTPSPort: 3129, FTPServer: "ftp.test",
			FTPPort: 2121}},
		{Type: config.TypeOpenVPN, Enabled: true, OpenVPN: &config.OpenVPNOptions{
			Config: sampleOpenVPN, UseDNS: true}},
		{Type: config.TypeAmneziaWG, AmneziaWG: &config.AmneziaWGOptions{Config: sampleTunnel,
			UseDNS: true}}} {
		_, err := result.AddOutbound(outbound)
		require.NoError(t, err)
	}
	for _, fallback := range []config.Fallback{
		{Enabled: true, HealthCheck: true, Outbounds: []string{"SOCKS5 1", "HTTP 1", "AmneziaWG 1"}},
		{Enabled: true, Outbounds: []string{"AmneziaWG 1"}},
		{Outbounds: []string{"HOST 1"}}} {
		_, err := result.AddFallback(fallback)
		require.NoError(t, err)
	}
	for _, route := range []config.Route{
		{ID: "corp", Directive: "DOMAIN-SUFFIX", Target: "OpenVPN 1", Enabled: true,
			Params: []string{"corp.test"}},
		{Directive: "DOMAIN", Target: "OpenVPN 1", Enabled: true, Params: []string{"wiki.test"}},
		{Directive: "IP-CIDR", Target: "Fallback 1", Enabled: true, Params: []string{"10.1.2.3/8"}},
		{Directive: "PROCESS-NAME", Target: "HOST 3", Enabled: true, Params: []string{"curl"}},
		{Directive: "DOMAIN", Target: "AmneziaWG 1", Enabled: true, Params: []string{"off.test"}},
		{Directive: "DOMAIN", Target: "Fallback 2", Enabled: true, Params: []string{"none.test"}},
		{Directive: "DOMAIN", Target: "HOST 2", Params: []string{"disabled.test"}}} {
		_, err := result.AddRoute(route)
		require.NoError(t, err)
	}
	result.Settings.DefaultTarget = "Fallback 3"
	require.NoError(t, result.Validate())
	return result
}

func TestGenerate_DNSDefault(t *testing.T) {
	snapshot := config.Default()
	for _, outbound := range []config.Outbound{
		{Type: config.TypeSocks5, ID: "Down", Enabled: true, Socks5: &config.Socks5Options{
			Server: "10.0.0.1", Port: 1080, UDP: true},
			DNS: config.DNSOptions{Servers: []string{"8.8.4.4"}}},
		{Type: config.TypeSocks5, ID: "Up", Enabled: true, Socks5: &config.Socks5Options{
			Server: "10.0.0.2", Port: 1080, UDP: true},
			DNS: config.DNSOptions{Servers: []string{"8.8.8.8"}}}} {
		_, err := snapshot.AddOutbound(outbound)
		require.NoError(t, err)
	}
	_, err := snapshot.AddFallback(config.Fallback{Enabled: true, Outbounds: []string{"Down", "Up"}})
	require.NoError(t, err)
	links := map[string]link.Status{"Down": {State: link.StateDown}, "Up": {State: link.StateUp}}
	build := func() *dnsSection {
		require.NoError(t, snapshot.Validate())
		data, warnings, err := Generate(snapshot, links, Options{Socket: "socket"})
		require.NoError(t, err)
		require.Empty(t, warnings)
		result := document{}
		require.NoError(t, json.Unmarshal(data, &result))
		return result.DNS
	}
	{
		snapshot.Settings.DNS = config.DNSOptions{Servers: []string{"1.1.1.1",
			"https://dns.example.com/dns-query"}}
		require.Equal(t, &dnsSection{Enable: true, Bootstrap: []string{"1.1.1.1"},
			Nameserver: []string{"1.1.1.1", "https://dns.example.com/dns-query"},
			Policy:     policyList{}}, build())
		snapshot.Settings.DNS = config.DNSOptions{Servers: []string{"tls://dns.example.com"},
			Exit: config.ViaRouter}
		require.Equal(t, &dnsSection{Enable: true, Bootstrap: []string{"system"},
			Nameserver: []string{"tls://dns.example.com#RULES"}, Policy: policyList{}}, build())
		snapshot.Settings.DNS = config.DNSOptions{Servers: []string{"1.1.1.1"}, Exit: "Fallback 1"}
		require.Equal(t, &dnsSection{Enable: true, Bootstrap: []string{"1.1.1.1#Fallback 1"},
			Nameserver: []string{"1.1.1.1#Fallback 1"}, Policy: policyList{}}, build())
	}
	{
		snapshot.Settings.DNS = config.DNSOptions{}
		snapshot.Settings.DefaultTarget = "Fallback 1"
		require.Equal(t, &dnsSection{Enable: true, Bootstrap: []string{"system"},
			Nameserver: []string{"8.8.8.8#Up"}, Policy: policyList{}}, build())
		links["Up"] = link.Status{State: link.StateDown}
		require.Equal(t, []string{"8.8.4.4#Down"}, build().Nameserver)
		snapshot.Settings.DefaultTarget = "Down"
		_, err := snapshot.AddRoute(config.Route{Directive: "DOMAIN", Target: "DIRECT",
			Enabled: true, Params: []string{"direct.test"}})
		require.NoError(t, err)
		result := build()
		require.Equal(t, []string{"8.8.4.4#Down"}, result.Nameserver)
		require.Equal(t, policyList{{"direct.test", []string{"system"}}}, result.Policy)
	}
}

func TestGenerate_DNSPins(t *testing.T) {
	proxy := config.Outbound{Type: config.TypeSocks5, ID: "P", Enabled: true,
		Socks5: &config.Socks5Options{Server: "proxy.corp.test", Port: 1080, UDP: true}}
	tunnel := config.Outbound{Type: config.TypeOpenVPN, ID: "V", Enabled: true,
		OpenVPN: &config.OpenVPNOptions{Config: "client\nremote vpn.other.test 1194\n", UseDNS: true}}
	links := map[string]link.Status{"V": {State: link.StateUp, DNS: []string{"10.8.0.1"}}}
	build := func(target string, outbounds []config.Outbound, routes ...config.Route) *dnsSection {
		snapshot := config.Default()
		for _, outbound := range outbounds {
			_, err := snapshot.AddOutbound(outbound)
			require.NoError(t, err)
		}
		for _, route := range routes {
			route.Enabled, route.Directive = true, "DOMAIN-SUFFIX"
			_, err := snapshot.AddRoute(route)
			require.NoError(t, err)
		}
		if target != "" {
			snapshot.Settings.DNS = config.DNSOptions{Servers: []string{"1.1.1.1"}, Exit: target}
		}
		require.NoError(t, snapshot.Validate())
		data, warnings, err := Generate(snapshot, links, Options{Socket: "socket"})
		require.NoError(t, err)
		require.Empty(t, warnings)
		result := document{}
		require.NoError(t, json.Unmarshal(data, &result))
		return result.DNS
	}
	{
		proxy.DNS = config.DNSOptions{Servers: []string{"10.0.0.53"}}
		result := build("", []config.Outbound{proxy},
			config.Route{Target: "P", Params: []string{"corp.test"}})
		require.Equal(t, policyList{{"proxy.corp.test", []string{"system"}},
			{"+.corp.test", []string{"10.0.0.53#P"}}}, result.Policy)
		require.Equal(t, []string{"system"}, result.Nameserver)
	}
	{
		proxy.DNS = config.DNSOptions{}
		result := build("", []config.Outbound{proxy, tunnel},
			config.Route{Target: "V", Params: []string{"corp.test"}})
		require.Equal(t, policyList{{"proxy.corp.test", []string{"10.8.0.1#V"}},
			{"+.corp.test", []string{"10.8.0.1#V"}}}, result.Policy)
	}
	{
		proxy.DNS = config.DNSOptions{Servers: []string{"8.8.8.8"}}
		routes := []config.Route{{Target: "V", Params: []string{"corp.test"}},
			{Target: "P", Params: []string{"other.test"}}}
		result := build("", []config.Outbound{tunnel, proxy}, routes...)
		require.Equal(t, policyList{{"vpn.other.test", []string{"system"}},
			{"proxy.corp.test", []string{"10.8.0.1#V"}}, {"+.corp.test", []string{"10.8.0.1#V"}},
			{"+.other.test", []string{"8.8.8.8#P"}}}, result.Policy)
		result = build("", []config.Outbound{proxy, tunnel}, routes...)
		require.Equal(t, policyList{{"proxy.corp.test", []string{"system"}},
			{"vpn.other.test", []string{"8.8.8.8#P"}}, {"+.corp.test", []string{"10.8.0.1#V"}},
			{"+.other.test", []string{"8.8.8.8#P"}}}, result.Policy)
	}
	{
		proxy.DNS = config.DNSOptions{}
		second := config.Outbound{Type: config.TypeHTTP, ID: "Q", Enabled: true,
			HTTP: &config.HTTPOptions{Server: "proxy.corp.test", Port: 3128, Single: true}}
		result := build("P", []config.Outbound{proxy, second})
		require.Equal(t, policyList{{"proxy.corp.test", []string{"system"}}}, result.Policy)
		require.Equal(t, []string{"1.1.1.1#P"}, result.Nameserver)
		require.Equal(t, []string{"1.1.1.1#P"}, result.Bootstrap)
		result = build("Q", []config.Outbound{proxy, second})
		require.Equal(t, policyList{{"proxy.corp.test", []string{"system"}}}, result.Policy)
		require.Equal(t, []string{"tcp://1.1.1.1#Q"}, result.Nameserver)
	}
}

func TestGenerate_DNSPolicy(t *testing.T) {
	snapshot := config.Default()
	for _, outbound := range []config.Outbound{
		{Type: config.TypeOpenVPN, Enabled: true, OpenVPN: &config.OpenVPNOptions{
			Config: "client\nremote 203.0.113.1 1194\n", UseDNS: true}},
		{Type: config.TypeSocks5, Enabled: true, Socks5: &config.Socks5Options{Server: "10.0.0.1",
			Port: 1080, UDP: true}, DNS: config.DNSOptions{Servers: []string{"8.8.8.8"}}},
		{Type: config.TypeHost, Enabled: true}} {
		_, err := snapshot.AddOutbound(outbound)
		require.NoError(t, err)
	}
	for _, route := range []config.Route{
		{Directive: "DOMAIN-SUFFIX", Target: "DIRECT", Params: []string{"www.corp.test"}},
		{Directive: "DOMAIN-SUFFIX", Target: "OpenVPN 1",
			Params: []string{"corp.test", "deep.www.corp.test"}},
		{Directive: "DOMAIN", Target: "SOCKS5 1", Params: []string{"corp.test"}},
		{Directive: "GEOSITE", Target: "DIRECT", Params: []string{"google"}},
		{Directive: "DOMAIN", Target: "SOCKS5 1", Params: []string{"a.test"}},
		{Directive: "DOMAIN", Target: "OpenVPN 1", Params: []string{"a.test"}},
		{Directive: "DOMAIN-KEYWORD", Target: "SOCKS5 1", Params: []string{"key"}},
		{Directive: "GEOSITE", Target: "SOCKS5 1", Params: []string{"google"}},
		{Directive: "DOMAIN-SUFFIX", Target: "DIRECT", Params: []string{"tail.test"}},
		{Directive: "DOMAIN-SUFFIX", Target: "HOST 1", Params: []string{"b.a.test"}}} {
		route.Enabled = true
		_, err := snapshot.AddRoute(route)
		require.NoError(t, err)
	}
	links := map[string]link.Status{"OpenVPN 1": {State: link.StateUp,
		DNS: []string{"10.8.0.1"}, Domains: []string{"vpn.test", "corp.test"}}}
	tunnel, proxy, system := []string{"10.8.0.1#OpenVPN 1"}, []string{"8.8.8.8#SOCKS5 1"},
		[]string{"system"}
	{
		data, warnings, err := Generate(snapshot, links, Options{Socket: "socket", Capture: true})
		require.NoError(t, err)
		require.Empty(t, warnings)
		result := document{}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, &dnsSection{Enable: true, EnhancedMode: "fake-ip",
			FakeIPRange: link.CaptureFakeIPRange, Bootstrap: system, Nameserver: system,
			Policy: policyList{{"+.www.corp.test", system}, {"+.corp.test", tunnel},
				{"geosite:google", system}, {"a.test", proxy}, {"+.vpn.test", tunnel}}}, result.DNS)
		require.Contains(t, string(data), `"+.www.corp.test": [`)
		require.Less(t, strings.Index(string(data), `"+.www.corp.test"`),
			strings.Index(string(data), `"+.corp.test"`))
		require.Less(t, strings.Index(string(data), `"geosite:google"`),
			strings.Index(string(data), `"a.test"`))
	}
	{
		snapshot.Settings.DefaultTarget = "SOCKS5 1"
		data, _, err := Generate(snapshot, links, Options{Socket: "socket"})
		require.NoError(t, err)
		result := document{}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, &dnsSection{Enable: true, Bootstrap: system, Nameserver: proxy,
			Policy: policyList{{"+.www.corp.test", system}, {"+.corp.test", tunnel},
				{"geosite:google", system}, {"+.tail.test", system}, {"+.b.a.test", system},
				{"+.vpn.test", tunnel}}}, result.DNS)
	}
	{
		snapshot.Outbounds[0].OpenVPN.UseDNS = false
		data, _, err := Generate(snapshot, links, Options{Socket: "socket"})
		require.NoError(t, err)
		result := document{}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, policyList{{"+.www.corp.test", system}, {"+.corp.test", system},
			{"geosite:google", system}, {"+.tail.test", system}, {"+.b.a.test", system}},
			result.DNS.Policy)
	}
}

func TestGenerate_DNSServers(t *testing.T) {
	snapshot := config.Default()
	for _, outbound := range []config.Outbound{
		{Type: config.TypeSocks5, Enabled: true, Socks5: &config.Socks5Options{Server: "10.0.0.1",
			Port: 1080, UDP: true}, DNS: config.DNSOptions{Servers: []string{"8.8.8.8",
			"https://dns.google/dns-query", "tls://1.1.1.1", "quic://dns.adguard-dns.com"}}},
		{Type: config.TypeSocks5, Enabled: true, Socks5: &config.Socks5Options{Server: "10.0.0.2",
			Port: 1080}, DNS: config.DNSOptions{Servers: []string{"9.9.9.9", "udp://9.9.9.10",
			"quic://9.9.9.11", "[2001:db8::1]:5353", "tls://9.9.9.9"}}},
		{Type: config.TypeHTTP, Enabled: true, HTTP: &config.HTTPOptions{Server: "10.0.0.3",
			Port: 3128, HTTPSServer: "10.0.0.4", HTTPSPort: 3129}, DNS: config.DNSOptions{
			Servers: []string{"https://1.1.1.1/dns-query", "1.0.0.1", "tcp://1.0.0.2:5353"}}},
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.1.1"}, Exit: config.ViaRouter}},
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.2.1", "2001:db8::2"}, Exit: config.ViaHost}},
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.3.1"}, Exit: "SOCKS5 2"}},
		{Type: config.TypeHost}} {
		_, err := snapshot.AddOutbound(outbound)
		require.NoError(t, err)
	}
	for _, fallback := range []config.Fallback{{Enabled: true, Outbounds: []string{"SOCKS5 1",
		"SOCKS5 2"}}, {Outbounds: []string{"HOST 1"}}, {Enabled: true, Outbounds: []string{"HOST 4"}}} {
		_, err := snapshot.AddFallback(fallback)
		require.NoError(t, err)
	}
	for _, outbound := range []config.Outbound{
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.5.1"}, Exit: "HOST 4"}},
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.6.1", "https://1.1.1.1/dns-query"}, Exit: "Fallback 1"}},
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.7.1"}, Exit: "Fallback 2"}},
		{Type: config.TypeHost, Enabled: true, DNS: config.DNSOptions{
			Servers: []string{"192.168.8.1"}, Exit: "Fallback 3"}}} {
		_, err := snapshot.AddOutbound(outbound)
		require.NoError(t, err)
	}
	snapshot.Outbounds[8].DNS.Servers = append(snapshot.Outbounds[8].DNS.Servers, "broken")
	require.Equal(t, map[string][]string{
		"SOCKS5 1": {"8.8.8.8#SOCKS5 1", "https://dns.google/dns-query#SOCKS5 1",
			"tls://1.1.1.1#SOCKS5 1", "quic://dns.adguard-dns.com#SOCKS5 1"},
		"SOCKS5 2": {"tcp://9.9.9.9#SOCKS5 2", "tcp://[2001:db8::1]:5353#SOCKS5 2",
			"tls://9.9.9.9#SOCKS5 2"},
		"HTTP 1": {"https://1.1.1.1/dns-query#HTTP 1~https", "tcp://1.0.0.1#HTTP 1",
			"tcp://1.0.0.2:5353#HTTP 1"},
		"HOST 1": {"192.168.1.1#RULES"}, "HOST 2": {"192.168.2.1", "[2001:db8::2]"},
		"HOST 3": {"tcp://192.168.3.1#SOCKS5 2"},
		"HOST 6": {"tcp://192.168.6.1#Fallback 1", "https://1.1.1.1/dns-query#Fallback 1"}},
		Resolvers(snapshot, nil))
	_, warnings, err := Generate(snapshot, nil, Options{Socket: "socket"})
	require.NoError(t, err)
	require.Equal(t, []string{"fallback 'Fallback 3' is skipped: no enabled outbounds",
		"DNS server 'udp://9.9.9.10' of outbound 'SOCKS5 2' is skipped: 'SOCKS5 2' does not carry UDP",
		"DNS server 'quic://9.9.9.11' of outbound 'SOCKS5 2' is skipped: 'SOCKS5 2' does not carry UDP",
		"DNS servers of outbound 'HOST 5' are skipped: exit 'HOST 4' is disabled or does not exist",
		"DNS server 'broken' of outbound 'HOST 6' is skipped: invalid DNS server 'broken', " +
			"expected IP address or URL",
		"DNS servers of outbound 'HOST 7' are skipped: exit 'Fallback 2' is disabled or does not exist",
		"DNS servers of outbound 'HOST 8' are skipped: exit 'Fallback 3' is disabled or does not exist"},
		warnings)
}

func TestGenerate_Document(t *testing.T) {
	snapshot := makeSnapshot(t)
	links := map[string]link.Status{"OpenVPN 1": {DNS: []string{"10.99.0.1", "fd00::1"},
		Domains: []string{"vpn.test", ""}}, "AmneziaWG 1": {DNS: []string{"10.0.0.53"}}}
	data, warnings, err := Generate(snapshot, links, Options{Socket: "/run/proximity/mihomo.sock"})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, 7899, result.MixedPort)
	require.True(t, result.AllowLan)
	require.Equal(t, "*", result.BindAddress)
	require.Equal(t, "rule", result.Mode)
	require.Equal(t, "warning", result.LogLevel)
	require.Equal(t, "/run/proximity/mihomo.sock", result.Controller)
	require.Equal(t, "strict", result.FindProcess)
	require.Zero(t, result.RoutingMark)
	require.Nil(t, result.Tun)
	require.Nil(t, result.Sniffer)
	{
		expected := []map[string]any{
			{"name": "HOST 1", "type": "direct", "udp": true},
			{"name": "HOST 2", "type": "direct", "udp": true, "interface-name": "eth1"},
			{"name": "HOST 3", "type": "direct", "udp": true, "routing-mark": 51003.0},
			{"name": "SOCKS5 1", "type": "socks5", "server": "10.0.0.1", "port": 1080.0,
				"udp": true, "username": "user", "password": "pass", "tls": true,
				"skip-cert-verify": true},
			{"name": "HTTP 1~https", "type": "http", "server": "secure.test", "port": 3129.0},
			{"name": "HTTP 1~ftp", "type": "http", "server": "ftp.test", "port": 2121.0},
			{"name": "HTTP 1", "type": "http", "server": "proxy.test", "port": 3128.0},
			{"name": "OpenVPN 1", "type": "direct", "udp": true, "routing-mark": 51006.0}}
		require.Equal(t, expected, result.Proxies)
	}
	{
		require.Len(t, result.Groups, 3)
		require.Equal(t, proxyGroup{Name: "Fallback 1", Type: "fallback",
			Proxies: []string{"SOCKS5 1", "HTTP 1"}, URL: "https://www.gstatic.com/generate_204",
			Interval: 60, Timeout: 5000}, result.Groups[0])
		require.Equal(t, "Fallback 1~https", result.Groups[1].Name)
		require.Equal(t, []string{"SOCKS5 1", "HTTP 1~https"}, result.Groups[1].Proxies)
		require.Equal(t, "Fallback 1~ftp", result.Groups[2].Name)
		require.Equal(t, []string{"SOCKS5 1", "HTTP 1~ftp"}, result.Groups[2].Proxies)
	}
	{
		require.Equal(t, []string{"DOMAIN-SUFFIX,corp.test,OpenVPN 1", "DOMAIN,wiki.test,OpenVPN 1",
			"AND,((IP-CIDR,10.0.0.0/8),(DST-PORT,443)),Fallback 1~https",
			"AND,((IP-CIDR,10.0.0.0/8),(DST-PORT,21)),Fallback 1~ftp",
			"IP-CIDR,10.0.0.0/8,Fallback 1", "PROCESS-NAME,curl,HOST 3", "MATCH,DIRECT"},
			result.Rules)
		servers := []string{"10.99.0.1#OpenVPN 1", "[fd00::1]#OpenVPN 1"}
		require.Equal(t, &dnsSection{Enable: true, Bootstrap: []string{"system"},
			Nameserver: []string{"system"}, Policy: policyList{{"+.corp.test", servers},
				{"wiki.test", servers}, {"+.vpn.test", servers}}}, result.DNS)
	}
	{
		require.Equal(t, []string{"fallback 'Fallback 2' is skipped: no enabled outbounds",
			"route 'Route 4' is skipped: target 'AmneziaWG 1' is disabled or does not exist",
			"route 'Route 5' is skipped: target 'Fallback 2' is disabled or does not exist",
			"default target 'Fallback 3' is disabled, using 'DIRECT'"}, warnings)
	}
}

func TestGenerate_Inbounds(t *testing.T) {
	snapshot := makeSnapshot(t)
	now := time.Unix(1700000000, 0)
	users := []config.InboundUser{{Enabled: true, Name: "alice", Password: "one"},
		{Name: "off", Password: "two"}}
	for _, inbound := range []config.Inbound{
		{Type: config.TypeSocks5, Enabled: true, Users: users},
		{Type: config.TypeSocks5, Enabled: true, Anonymous: true, Users: users,
			Outbound: config.ViaHost, Socks5: &config.Socks5Server{Listen: "10.0.0.5", TLS: true}},
		{Type: config.TypeSocks5, Enabled: true, Anonymous: true, Outbound: "HTTP 1"},
		{Type: config.TypeSocks5, Enabled: true, Outbound: "HOST 1"},
		{Type: config.TypeSocks5, Enabled: true, Anonymous: true, Outbound: "AmneziaWG 1"},
		{Type: config.TypeSocks5, Anonymous: true},
		{Type: config.TypeOpenVPN, Enabled: true, Anonymous: true, Outbound: "HTTP 1"},
		{Type: config.TypeOpenVPN, Enabled: true, Users: users}} {
		_, err := snapshot.AddInbound(inbound, now)
		require.NoError(t, err)
	}
	data, warnings, err := Generate(snapshot, nil, Options{Socket: "/run/mihomo.sock"})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Listeners, 3)
	require.Equal(t, listener{Name: "SOCKS5 1", Type: "socks", Port: 7800, Listen: "0.0.0.0",
		UDP: true, Users: []listenerUser{{"alice", "one"}}}, result.Listeners[0])
	secure := snapshot.Inbounds[1].Socks5
	require.Equal(t, listener{Name: "SOCKS5 2", Type: "socks", Port: 7801, Listen: "10.0.0.5",
		Users: []listenerUser{}, Certificate: secure.Certificate, PrivateKey: secure.Key},
		result.Listeners[1])
	require.NotEmpty(t, secure.Certificate)
	require.Equal(t, []listenerUser{}, result.Listeners[2].Users)
	require.Contains(t, string(data), `"users": []`)
	require.Equal(t, []string{"IN-NAME,SOCKS5 2,DIRECT",
		"AND,((IN-NAME,SOCKS5 3),(DST-PORT,443)),HTTP 1~https",
		"AND,((IN-NAME,SOCKS5 3),(DST-PORT,21)),HTTP 1~ftp", "IN-NAME,SOCKS5 3,HTTP 1",
		"AND,((SRC-IP-CIDR,10.77.7.0/24),(DST-PORT,443)),HTTP 1~https",
		"AND,((SRC-IP-CIDR,10.77.7.0/24),(DST-PORT,21)),HTTP 1~ftp",
		"SRC-IP-CIDR,10.77.7.0/24,HTTP 1"}, result.Rules[:7])
	require.NotContains(t, string(data), "10.77.8.0/24,")
	require.Contains(t, warnings, "inbound 'SOCKS5 4' is skipped: anonymous connections are "+
		"not allowed and enabled users do not exist")
	require.Contains(t, warnings, "inbound 'SOCKS5 5' is skipped: outbound 'AmneziaWG 1' is disabled")
	require.NotNil(t, result.Tun)
	require.Equal(t, link.CaptureDevice, result.Tun.Device)
	require.False(t, result.Tun.AutoRoute)
	require.Zero(t, result.RoutingMark)
	require.NotNil(t, result.Sniffer)
	require.Equal(t, "fake-ip", result.DNS.EnhancedMode)
	{
		snapshot.Settings.IPv6 = true
		for index := range snapshot.Inbounds {
			snapshot.Inbounds[index].Enabled = index == 0
		}
		data, warnings, err := Generate(snapshot, nil, Options{Socket: "/run/mihomo.sock"})
		require.NoError(t, err)
		result := document{}
		require.NoError(t, json.Unmarshal(data, &result))
		require.Len(t, result.Listeners, 1)
		require.Equal(t, "::", result.Listeners[0].Listen)
		require.Nil(t, result.Tun)
		for _, warning := range warnings {
			require.NotContains(t, warning, "inbound")
		}
	}
}

func TestGenerate_Minimal(t *testing.T) {
	snapshot := config.Default()
	snapshot.Routes = []config.Route{{Key: "k", Directive: "DOMAIN", Target: "DIRECT",
		Enabled: true, Params: []string{"bad domain"}}}
	data, warnings, err := Generate(snapshot, nil, Options{Socket: "socket"})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, []string{"MATCH,DIRECT"}, result.Rules)
	require.Equal(t, "off", result.FindProcess)
	require.Equal(t, &dnsSection{Enable: true, Bootstrap: []string{"system"},
		Nameserver: []string{"system"}, Policy: policyList{}}, result.DNS)
	require.Empty(t, result.Proxies)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "route #1 is skipped: failed to parse line 1")
}

func TestGenerate_Capture(t *testing.T) {
	snapshot := config.Default()
	data, _, err := Generate(snapshot, nil, Options{Socket: "socket", Capture: true,
		ProxyBind: "127.0.0.1"})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, "127.0.0.1", result.BindAddress)
	require.True(t, result.AllowLan)
	require.Equal(t, 51000, result.RoutingMark)
	require.Equal(t, &tunSection{Enable: true, Device: "proximity_tun", Stack: "gvisor",
		DNSHijack: []string{"any:53"}}, result.Tun)
	require.True(t, result.Sniffer.Enable)
	require.False(t, result.Sniffer.OverrideDest)
	require.Equal(t, sniffPorts{Ports: []string{"443", "8443"}}, result.Sniffer.Sniff["TLS"])
	require.Equal(t, sniffPorts{Ports: []string{"80", "8080-8880"}}, result.Sniffer.Sniff["HTTP"])
	require.Contains(t, result.Sniffer.Sniff, "QUIC")
	require.Equal(t, &dnsSection{Enable: true, EnhancedMode: "fake-ip",
		FakeIPRange: link.CaptureFakeIPRange, Bootstrap: []string{"system"},
		Nameserver: []string{"system"}, Policy: policyList{}}, result.DNS)
}

func TestGenerate_CaptureProxyDNS(t *testing.T) {
	snapshot := config.Default()
	_, err := snapshot.AddOutbound(config.Outbound{Type: config.TypeSocks5, Enabled: true,
		Socks5: &config.Socks5Options{Server: "127.0.0.1", Port: 12007}})
	require.NoError(t, err)
	_, err = snapshot.AddRoute(config.Route{Directive: "DOMAIN-SUFFIX", Target: "SOCKS5 1",
		Enabled: true, Params: []string{"pki.elvees.com"}})
	require.NoError(t, err)
	data, _, err := Generate(snapshot, nil, Options{Socket: "socket", Capture: true})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Contains(t, result.Rules, "DOMAIN-SUFFIX,pki.elvees.com,SOCKS5 1")
	require.Equal(t, "fake-ip", result.DNS.EnhancedMode)
	require.Empty(t, result.DNS.Policy)
}

func TestGenerate_Probe(t *testing.T) {
	snapshot := makeSnapshot(t)
	options := Options{Socket: "socket", ProbePort: 17897, ProbeSecret: "secret"}
	data, _, err := Generate(snapshot, nil, options)
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	probe := result.Listeners[len(result.Listeners)-1]
	require.Equal(t, ProbeListener, probe.Name)
	require.Equal(t, 17897, probe.Port)
	require.Equal(t, "127.0.0.1", probe.Listen)
	layout := Describe(snapshot, nil, options)
	require.Len(t, layout.Origins, len(result.Rules))
	require.Len(t, probe.Users, len(layout.Probe))
	require.Equal(t, "probe0", layout.Probe[config.TargetDirect])
	require.NotContains(t, layout.Probe, "AmneziaWG 1")
	user := layout.Probe["SOCKS5 1"]
	require.Contains(t, probe.Users, listenerUser{user, "secret"})
	require.Contains(t, result.Rules, "AND,((IN-NAME,proximity-probe),(IN-USER,"+user+")),SOCKS5 1")
	require.Equal(t, "IN-NAME,proximity-probe,REJECT", result.Rules[len(layout.Probe)])
	require.Equal(t, make([]string, len(layout.Probe)+1), layout.Origins[:len(layout.Probe)+1])
	{
		data, _, err := Generate(snapshot, nil, Options{Socket: "socket"})
		require.NoError(t, err)
		require.NotContains(t, string(data), ProbeListener)
	}
}

func TestGenerate_SelectGroups(t *testing.T) {
	snapshot := makeSnapshot(t)
	snapshot.Fallbacks[0].HealthCheck = false
	links := map[string]link.Status{"SOCKS5 1": {State: link.StateDown},
		"HTTP 1": {State: link.StateUp}}
	data, _, err := Generate(snapshot, links, Options{Socket: "socket"})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Len(t, result.Groups, 3)
	require.Equal(t, proxyGroup{Name: "Fallback 1", Type: "select", Proxies: []string{"HTTP 1"}},
		result.Groups[0])
	require.Equal(t, proxyGroup{Name: "Fallback 1~https", Type: "select",
		Proxies: []string{"HTTP 1~https"}}, result.Groups[1])
	require.NotContains(t, string(data), `"url"`)
	{
		links["HTTP 1"] = link.Status{State: link.StateFailed}
		data, _, err := Generate(snapshot, links, Options{Socket: "socket"})
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, []string{"SOCKS5 1", "HTTP 1"}, result.Groups[0].Proxies)
	}
	{
		data, _, err := Generate(snapshot, nil, Options{Socket: "socket"})
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &result))
		require.Equal(t, []string{"SOCKS5 1", "HTTP 1"}, result.Groups[0].Proxies)
	}
}

func TestItems_EnabledOnly(t *testing.T) {
	snapshot := makeSnapshot(t)
	result := Items(snapshot)
	require.Len(t, result, 6)
	require.Equal(t, "'corp'", result[0].Label)
	require.Equal(t, "'Route 1'", result[1].Label)
	require.Equal(t, "OpenVPN 1", result[1].Target)
	require.Equal(t, "10.0.0.0/8", result[2].Rules[0].Value)
	require.Equal(t, snapshot.Routes[5].Key, result[5].Key)
	{
		snapshot.Routes[0].Params = []string{"# corporate", "corp.test", "# old.test"}
		result := Items(snapshot)
		require.Equal(t, []rules.Rule{{Directive: rules.DomainSuffix, Value: "corp.test"}},
			result[0].Rules)
	}
}

func TestOrigins_Rules(t *testing.T) {
	snapshot := makeSnapshot(t)
	_, err := snapshot.AddInbound(config.Inbound{Type: config.TypeSocks5, Enabled: true,
		Anonymous: true, Outbound: "HTTP 1"}, time.Unix(1700000000, 0))
	require.NoError(t, err)
	_, err = snapshot.AddRoute(config.Route{Directive: "DOMAIN", Target: "HTTP 1", Enabled: true,
		Params: []string{"split.test"}})
	require.NoError(t, err)
	data, _, err := Generate(snapshot, nil, Options{})
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	origins := Describe(snapshot, nil, Options{}).Origins
	require.Len(t, origins, len(result.Rules))
	inbound, split := InboundOrigin("SOCKS5 1"), snapshot.Routes[len(snapshot.Routes)-1].Key
	fallback := snapshot.Routes[2].Key
	require.Equal(t, []string{inbound, inbound, inbound, snapshot.Routes[0].Key,
		snapshot.Routes[1].Key, fallback, fallback, fallback, snapshot.Routes[3].Key, split, split,
		split, OriginDefault}, origins)
	require.Equal(t, "DOMAIN,split.test,HTTP 1", result.Rules[11])
	require.True(t, strings.HasPrefix(result.Rules[12], "MATCH,"))
	require.Equal(t, "inbound:SOCKS5 1", inbound)
}

func TestOwner_Variants(t *testing.T) {
	require.Equal(t, "HTTP 1", Owner("HTTP 1"))
	require.Equal(t, "HTTP 1", Owner("HTTP 1~https"))
	require.Equal(t, "HTTP 1", Owner("HTTP 1~ftp"))
	require.Equal(t, "", Owner(""))
}

func TestPing_Rules(t *testing.T) {
	{
		snapshot := makeSnapshot(t)
		require.Equal(t, []link.PingRule{{Prefix: netip.MustParsePrefix("10.0.0.0/8")},
			{Allow: true}}, Ping(snapshot, nil))
	}
	{
		snapshot := makeSnapshot(t)
		_, err := snapshot.AddFallback(config.Fallback{Enabled: true,
			Outbounds: []string{"HOST 1", "HOST 2"}})
		require.NoError(t, err)
		for _, route := range []config.Route{
			{Directive: "IP-CIDR", Target: "HOST 2", Enabled: true, Params: []string{"2001:db8::/32"}},
			{Directive: "IP-CIDR", Target: "SOCKS5 1", Enabled: true, Params: []string{"1.1.1.1:443"}},
			{Directive: "IP-CIDR", Target: "OpenVPN 1", Enabled: true, Params: []string{"10.8.0.0/16"}},
			{Directive: "IP-CIDR", Target: "REJECT", Enabled: true, Params: []string{"192.0.2.1"}},
			{Directive: "IP-CIDR", Target: "Fallback 4", Enabled: true, Params: []string{"192.0.2.2"}},
			{Directive: "IP-CIDR", Target: "SOCKS5 1", Params: []string{"192.0.2.3"}},
			{Directive: "GEOIP", Target: "DIRECT", Enabled: true, Params: []string{"RU"}},
			{Directive: "GEOIP", Target: "SOCKS5 1", Enabled: true, Params: []string{"CN"}},
			{Directive: "IP-CIDR", Target: "DIRECT", Enabled: true, Params: []string{"192.0.2.4"}}} {
			_, err := snapshot.AddRoute(route)
			require.NoError(t, err)
		}
		require.Equal(t, []link.PingRule{{Prefix: netip.MustParsePrefix("10.0.0.0/8")},
			{Prefix: netip.MustParsePrefix("2001:db8::/32"), Allow: true},
			{Prefix: netip.MustParsePrefix("10.8.0.0/16"), Table: link.Table(snapshot.Outbounds[5].Slot)},
			{Prefix: netip.MustParsePrefix("192.0.2.1/32")},
			{Prefix: netip.MustParsePrefix("192.0.2.2/32"), Allow: true}, {}}, Ping(snapshot, nil))
	}
	{
		snapshot := makeSnapshot(t)
		snapshot.Settings.DefaultTarget = "SOCKS5 1"
		require.Equal(t, link.PingRule{}, Ping(snapshot, nil)[1])
		snapshot.Settings.DefaultTarget = "HOST 3"
		require.Equal(t, link.PingRule{Allow: true}, Ping(snapshot, nil)[1])
		snapshot.Settings.DefaultTarget = "OpenVPN 1"
		require.Equal(t, link.PingRule{Table: link.Table(snapshot.Outbounds[5].Slot)},
			Ping(snapshot, nil)[1])
		snapshot.Settings.DefaultTarget = "AmneziaWG 1"
		require.Equal(t, link.PingRule{Allow: true}, Ping(snapshot, nil)[1])
	}
}

func TestPolicyList_JSON(t *testing.T) {
	list := policyList{{"+.z.test", []string{"1.1.1.1#A B"}}, {"geosite:x", []string{"system"}},
		{"a.test", []string{"tcp://[2001:db8::1]:5353", "8.8.8.8"}}}
	data, err := json.Marshal(list)
	require.NoError(t, err)
	require.Equal(t, `{"+.z.test":["1.1.1.1#A B"],"geosite:x":["system"],`+
		`"a.test":["tcp://[2001:db8::1]:5353","8.8.8.8"]}`, string(data))
	result := policyList{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, list, result)
	data, err = json.Marshal(policyList{})
	require.NoError(t, err)
	require.Equal(t, "{}", string(data))
	{
		require.Error(t, result.UnmarshalJSON([]byte(``)))
		require.ErrorContains(t, result.UnmarshalJSON([]byte(`{"a":5}`)),
			"failed to decode policy servers of 'a'")
		require.ErrorContains(t, result.UnmarshalJSON([]byte(`{"a":["b"],`)),
			"failed to decode policy key")
	}
}

func TestUsed_Targets(t *testing.T) {
	snapshot := makeSnapshot(t)
	require.Equal(t, map[string]bool{"Fallback 3": true, "OpenVPN 1": true, "Fallback 1": true,
		"HOST 3": true, "AmneziaWG 1": true, "Fallback 2": true, "SOCKS5 1": true, "HTTP 1": true},
		Used(snapshot))
}
