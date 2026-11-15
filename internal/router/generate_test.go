package router

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
	"proximity/internal/link"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = vpn.example.com:51820\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\n"

func makeSnapshot(t *testing.T) config.Config {
	result := config.Default()
	for _, endpoint := range []config.Endpoint{{Type: config.TypeHost, Enabled: true},
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
		_, err := result.AddEndpoint(endpoint)
		require.NoError(t, err)
	}
	for _, fallback := range []config.Fallback{
		{Enabled: true, Endpoints: []string{"SOCKS5 1", "HTTP 1", "AmneziaWG 1"}},
		{Enabled: true, Endpoints: []string{"AmneziaWG 1"}},
		{Endpoints: []string{"HOST 1"}}} {
		_, err := result.AddFallback(fallback)
		require.NoError(t, err)
	}
	for _, filter := range []config.Filter{
		{ID: "corp", Directive: "DOMAIN-SUFFIX", Target: "OpenVPN 1", Enabled: true,
			Params: []string{"corp.test"}},
		{Directive: "DOMAIN", Target: "OpenVPN 1", Enabled: true, Params: []string{"wiki.test"}},
		{Directive: "IP-CIDR", Target: "Fallback 1", Enabled: true, Params: []string{"10.1.2.3/8"}},
		{Directive: "PROCESS-NAME", Target: "HOST 3", Enabled: true, Params: []string{"curl"}},
		{Directive: "DOMAIN", Target: "AmneziaWG 1", Enabled: true, Params: []string{"off.test"}},
		{Directive: "DOMAIN", Target: "Fallback 2", Enabled: true, Params: []string{"none.test"}},
		{Directive: "DOMAIN", Target: "HOST 2", Params: []string{"disabled.test"}}} {
		_, err := result.AddFilter(filter)
		require.NoError(t, err)
	}
	result.Settings.DefaultTarget = "Fallback 3"
	require.NoError(t, result.Validate())
	return result
}

func TestGenerate_Document(t *testing.T) {
	snapshot := makeSnapshot(t)
	links := map[string]link.Status{"OpenVPN 1": {DNS: []string{"10.99.0.1", "fd00::1"},
		Domains: []string{"vpn.test", ""}}, "AmneziaWG 1": {DNS: []string{"10.0.0.53"}}}
	data, warnings, err := Generate(snapshot, links, "/run/proximity/mihomo.sock")
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
		require.Equal(t, &dnsSection{Enable: true, Nameserver: []string{"system"},
			Policy: map[string][]string{"+.corp.test": servers, "wiki.test": servers,
				"+.vpn.test": servers}}, result.DNS)
	}
	{
		require.Equal(t, []string{"fallback 'Fallback 2' is skipped: no enabled endpoints",
			"filter #5 is skipped: target 'AmneziaWG 1' is disabled or does not exist",
			"filter #6 is skipped: target 'Fallback 2' is disabled or does not exist",
			"default target 'Fallback 3' is disabled, using 'DIRECT'"}, warnings)
	}
}

func TestGenerate_Minimal(t *testing.T) {
	snapshot := config.Default()
	snapshot.Filters = []config.Filter{{Key: "k", Directive: "DOMAIN", Target: "DIRECT",
		Enabled: true, Params: []string{"bad domain"}}}
	data, warnings, err := Generate(snapshot, nil, "socket")
	require.NoError(t, err)
	result := document{}
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, []string{"MATCH,DIRECT"}, result.Rules)
	require.Equal(t, "off", result.FindProcess)
	require.Nil(t, result.DNS)
	require.Empty(t, result.Proxies)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "filter #1 is skipped: failed to parse line 1")
}

func TestItems_EnabledOnly(t *testing.T) {
	snapshot := makeSnapshot(t)
	result := Items(snapshot)
	require.Len(t, result, 6)
	require.Equal(t, "'corp'", result[0].Label)
	require.Equal(t, "#2", result[1].Label)
	require.Equal(t, "OpenVPN 1", result[1].Target)
	require.Equal(t, "10.0.0.0/8", result[2].Rules[0].Value)
	require.Equal(t, snapshot.Filters[5].Key, result[5].Key)
}

func TestOwner_Variants(t *testing.T) {
	require.Equal(t, "HTTP 1", Owner("HTTP 1"))
	require.Equal(t, "HTTP 1", Owner("HTTP 1~https"))
	require.Equal(t, "HTTP 1", Owner("HTTP 1~ftp"))
	require.Equal(t, "", Owner(""))
}

func TestUsed_Targets(t *testing.T) {
	snapshot := makeSnapshot(t)
	require.Equal(t, map[string]bool{"Fallback 3": true, "OpenVPN 1": true, "Fallback 1": true,
		"HOST 3": true, "AmneziaWG 1": true, "Fallback 2": true, "SOCKS5 1": true, "HTTP 1": true},
		Used(snapshot))
}
