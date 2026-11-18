package config

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = vpn.example.com:51820\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\n"

func makeConfig(t *testing.T) *Config {
	result := Default()
	for _, outbound := range []Outbound{{Type: TypeHost, Enabled: true},
		{Type: TypeSocks5, Enabled: true, Socks5: &Socks5Options{Server: "10.0.0.1", Port: 1080}},
		{Type: TypeHTTP, Enabled: true, HTTP: &HTTPOptions{Server: "proxy.example.com", Port: 3128,
			HTTPSServer: "secure.example.com", HTTPSPort: 3129}},
		{Type: TypeOpenVPN, Enabled: true, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN}},
		{Type: TypeAmneziaWG, AmneziaWG: &AmneziaWGOptions{Config: sampleTunnel}}} {
		_, err := result.AddOutbound(outbound)
		require.NoError(t, err)
	}
	require.NoError(t, result.Validate())
	return &result
}

func unmarshalError(t *testing.T, data string, target any) string {
	err := json.Unmarshal([]byte(data), target)
	require.Error(t, err)
	return err.Error()
}

func TestAddFallback_Members(t *testing.T) {
	config := makeConfig(t)
	{
		fallback, err := config.AddFallback(Fallback{Enabled: true,
			Outbounds: []string{"SOCKS5 1", "HOST 1"}})
		require.NoError(t, err)
		require.Equal(t, Fallback{ID: "Fallback 1", Enabled: true, URL: defaultHealthURL,
			Interval: 60, Outbounds: []string{"SOCKS5 1", "HOST 1"}}, fallback)
		require.True(t, config.TargetExists("Fallback 1"))
	}
	for _, item := range []struct {
		fallback Fallback
		message  string
	}{
		{Fallback{ID: "Fallback 1"}, "ID 'Fallback 1' already exists"},
		{Fallback{ID: "HOST 1"}, "ID 'HOST 1' already exists"},
		{Fallback{ID: "REJECT"}, "ID 'REJECT' already exists"},
		{Fallback{ID: "x", Outbounds: []string{"missing"}}, "outbound 'missing' does not exist"},
		{Fallback{ID: "x", Outbounds: []string{"HOST 1", "HOST 1"}}, "duplicate outbound 'HOST 1'"},
		{Fallback{ID: "x", Interval: 5}, "invalid check interval (5)"},
		{Fallback{ID: "x", URL: "ftp://example.com"}, "invalid check URL 'ftp://example.com'"},
		{Fallback{ID: "x", URL: "http://%zz"}, "as URL"},
	} {
		_, err := config.AddFallback(item.fallback)
		require.ErrorContains(t, err, item.message)
	}
	{
		require.NoError(t, config.UpdateFallback("Fallback 1", Fallback{
			Outbounds: []string{"HOST 1"}}))
		require.Equal(t, "Fallback 1", config.Fallbacks[0].ID)
		require.False(t, config.Fallbacks[0].Enabled)
		require.False(t, config.Fallbacks[0].HealthCheck)
		require.ErrorContains(t, config.UpdateFallback("missing", Fallback{}), "does not exist")
		require.ErrorContains(t, config.UpdateFallback("Fallback 1", Fallback{Interval: 1}),
			"invalid check interval")
		require.ErrorContains(t, config.UpdateFallback("Fallback 1", Fallback{ID: "HOST 1"}),
			"ID 'HOST 1' already exists")
		require.ErrorContains(t, config.UpdateFallback("Fallback 1", Fallback{ID: "bad,id"}),
			"invalid ID 'bad,id'")
	}
	{
		config.Settings.DefaultTarget = "Fallback 1"
		_, err := config.AddFallback(Fallback{})
		require.NoError(t, err)
		require.NoError(t, config.MoveFallback("Fallback 2", -1))
		require.Equal(t, "Fallback 2", config.Fallbacks[0].ID)
		require.NoError(t, config.RemoveFallback("Fallback 1"))
		require.Equal(t, TargetDirect, config.Settings.DefaultTarget)
		require.ErrorContains(t, config.RemoveFallback("Fallback 1"), "does not exist")
		require.NoError(t, config.Validate())
	}
}

func TestAddInbound_Defaults(t *testing.T) {
	config := makeConfig(t)
	now := time.Unix(1700000000, 0)
	socks, err := config.AddInbound(Inbound{Type: TypeSocks5, Enabled: true, Autostart: true,
		Users: []InboundUser{{Enabled: true, Name: " alice ", Password: "secret"}}}, now)
	require.NoError(t, err)
	require.Equal(t, "SOCKS5 1", socks.ID)
	require.True(t, socks.Autostart)
	require.Equal(t, InboundPortFirst, socks.Port)
	require.Equal(t, 1, socks.Slot())
	require.Equal(t, ViaRouter, socks.Outbound)
	require.Empty(t, socks.Target())
	require.False(t, socks.Direct())
	require.Equal(t, &Socks5Server{Listen: ListenAny, UDP: true}, socks.Socks5)
	require.Equal(t, &Recovery{Enabled: true, Delay: 60}, socks.Recovery)
	require.Nil(t, socks.OpenVPN)
	require.Equal(t, []InboundUser{{Enabled: true, Name: "alice", Password: "secret"}},
		socks.ActiveUsers())
	server, err := config.AddInbound(Inbound{Type: TypeOpenVPN, Outbound: ViaHost,
		OpenVPN: &OpenVPNServer{OTP: true, Routes: []string{" 192.168.10.7/24 ", ""},
			DNS: []string{"10.0.0.53"}, Ciphers: "aes-256-gcm", OTPKey: "gezd-gnbv gy3t=="}}, now)
	require.NoError(t, err)
	require.Equal(t, "OpenVPN 1", server.ID)
	require.Equal(t, InboundPortFirst+1, server.Port)
	require.Equal(t, TargetDirect, server.Target())
	require.True(t, server.Direct())
	require.Equal(t, []InboundUser{}, server.Users)
	options := server.OpenVPN
	require.Equal(t, "10.77.2.0/24", options.Network)
	require.Equal(t, []string{"192.168.10.0/24"}, options.Routes)
	require.Equal(t, "AES-256-GCM", options.Ciphers)
	require.Equal(t, "GEZDGNBVGY3T", options.OTPKey)
	require.Equal(t, [8]any{ListenAny, ProtocolUDP, 32, 10, 60, 3600, 1420, 3},
		[8]any{options.Listen, options.Protocol, options.MaxClients, options.KeepaliveInterval,
			options.KeepaliveTimeout, options.Renegotiation, options.MTU, options.Verbosity})
	require.Equal(t, "SHA256 1.2", options.Digest+" "+options.TLSVersion)
	require.Equal(t, "Proximity CA", CommonName(options.CACertificate))
	require.Equal(t, "proximity-server", CommonName(options.Certificate))
	require.Equal(t, "proximity-client", CommonName(options.ClientCertificate))
	require.Regexp(t, `^-----BEGIN OpenVPN Static key V1-----\n([0-9a-f]{32}\n){16}-----END`,
		options.TLSCrypt)
	require.NoError(t, config.Validate())
	{
		third, err := config.AddInbound(Inbound{Type: TypeOpenVPN, ID: "Office", Port: 7810,
			Outbound: "HOST 1", OpenVPN: &OpenVPNServer{CACertificate: options.CACertificate,
				CAKey: options.CAKey, OTP: true}}, now)
		require.NoError(t, err)
		require.Equal(t, "10.77.11.0/24", third.OpenVPN.Network)
		require.Equal(t, "HOST 1", third.Target())
		require.Len(t, third.OpenVPN.OTPKey, 32)
		require.NotEqual(t, options.Certificate, third.OpenVPN.Certificate)
		require.NoError(t, checkIssued(third.OpenVPN.ClientCertificate, options.CACertificate))
		fourth, err := config.AddInbound(Inbound{Type: TypeSocks5, Socks5: &Socks5Server{
			Listen: "10.0.0.1", TLS: true}}, now)
		require.NoError(t, err)
		require.Equal(t, InboundPortFirst+2, fourth.Port)
		require.NoError(t, checkPair(fourth.Socks5.Certificate, fourth.Socks5.Key))
		require.NoError(t, config.Validate())
	}
	{
		clone := config.Clone()
		clone.Inbounds[0].Users[0].Name, clone.Inbounds[0].Socks5.Listen = "x", "10.0.0.9"
		clone.Inbounds[0].Recovery.Attempts = 9
		clone.Inbounds[1].OpenVPN.Routes[0], clone.Inbounds[1].OpenVPN.DNS[0] = "x", "x"
		require.Equal(t, "alice", config.Inbounds[0].Users[0].Name)
		require.Equal(t, ListenAny, config.Inbounds[0].Socks5.Listen)
		require.Equal(t, 0, config.Inbounds[0].Recovery.Attempts)
		require.Equal(t, "192.168.10.0/24", config.Inbounds[1].OpenVPN.Routes[0])
		require.Equal(t, "10.0.0.53", config.Inbounds[1].OpenVPN.DNS[0])
	}
	{
		full := Default()
		for port := InboundPortFirst; port <= InboundPortLast; port++ {
			full.Inbounds = append(full.Inbounds, Inbound{ID: "x", Port: port})
		}
		_, err := full.AddInbound(Inbound{Type: TypeSocks5}, now)
		require.EqualError(t, err, "free port does not exist in range 7800..7898")
	}
}

func TestAddInbound_Invalid(t *testing.T) {
	now := time.Unix(1700000000, 0)
	base := makeConfig(t)
	_, err := base.AddInbound(Inbound{Type: TypeOpenVPN, Anonymous: true}, now)
	require.NoError(t, err)
	valid := *base.Inbounds[0].OpenVPN
	other, err := GenerateMaterial(MaterialAll, Material{}, now)
	require.NoError(t, err)
	for _, item := range []struct {
		inbound Inbound
		message string
	}{
		{Inbound{Type: "http"}, "unexpected inbound type 'http'"},
		{Inbound{Type: TypeSocks5, ID: "OpenVPN 1"}, "ID 'OpenVPN 1' already exists"},
		{Inbound{Type: TypeSocks5, ID: "bad/id"}, "invalid ID 'bad/id'"},
		{Inbound{Type: TypeSocks5, Port: 7800}, "port (7800) is used by inbound 'OpenVPN 1'"},
		{Inbound{Type: TypeSocks5, Port: 7899}, "invalid port (7899), expected 7800..7898"},
		{Inbound{Type: TypeSocks5, Recovery: &Recovery{Delay: 86401}},
			"invalid recovery delay (86401), expected 1..86400"},
		{Inbound{Type: TypeSocks5, Recovery: &Recovery{Enabled: true, Attempts: 1001}},
			"invalid recovery attempts (1001), expected 0..1000"},
		{Inbound{Type: TypeSocks5, Outbound: "missing"}, "outbound 'missing' does not exist"},
		{Inbound{Type: TypeSocks5, Users: make([]InboundUser, 257)}, "invalid number of users (257)"},
		{Inbound{Type: TypeSocks5, Users: []InboundUser{{Name: "a b", Password: "x"}}},
			"invalid user name 'a b'"},
		{Inbound{Type: TypeSocks5, Users: []InboundUser{{Name: "a", Password: "with space"}}},
			"invalid password of user 'a'"},
		{Inbound{Type: TypeSocks5, Users: []InboundUser{{Name: "a", Password: "x"},
			{Name: "a", Password: "y"}}}, "duplicate user 'a'"},
		{Inbound{Type: TypeSocks5, Socks5: &Socks5Server{Listen: "::1"}},
			"invalid listen address '::1'"},
		{Inbound{Type: TypeSocks5, Socks5: &Socks5Server{TLS: true, Certificate: "x", Key: "y"}},
			"failed to check TLS certificate"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Server: "bad host"}},
			"invalid server address 'bad host'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{PublicPort: 70000}},
			"invalid public port (70000)"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Listen: "host"}},
			"invalid listen address 'host'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Protocol: "sctp"}},
			"unexpected protocol 'sctp'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Network: "10.0.0.0"}},
			`cannot parse "10.0.0.0" as network`},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Network: "10.0.0.0/30"}},
			"invalid network '10.0.0.0/30', expected IPv4 prefix /16../29"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Network: "198.19.0.0/24"}},
			"overlaps reserved network '198.18.0.0/15'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Network: "10.77.1.128/25"}},
			"network '10.77.1.128/25' overlaps network '10.77.1.0/24' of another inbound"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Routes: []string{"fd00::/64"}}},
			"invalid route 'fd00::/64', expected IPv4 prefix"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{DNS: []string{"dns.test"}}},
			"invalid DNS server 'dns.test', expected IPv4 address"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{DNS: []string{"1.1.1.1", "1.1.1.2",
			"1.1.1.3", "1.1.1.4", "1.1.1.5"}}}, "too many routes or DNS servers"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{MaxClients: 1001}},
			"invalid number of clients (1001)"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{KeepaliveInterval: 601}},
			"invalid keepalive interval (601)"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{KeepaliveInterval: 60,
			KeepaliveTimeout: 60}}, "invalid keepalive timeout (60), expected 61..3600"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Renegotiation: 119}},
			"invalid renegotiation period (119), expected 120..86400"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{MTU: 9000}}, "invalid MTU (9000)"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Verbosity: 7}},
			"invalid verbosity (7)"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Ciphers: "a;b"}},
			"invalid data ciphers 'A;B'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{Digest: "md5"}},
			"unexpected digest 'MD5'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{TLSVersion: "1.0"}},
			"unexpected TLS version '1.0'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{OTPKey: "18"}},
			"failed to decode OTP key"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: valid.CACertificate}},
			"failed to issue server certificate: failed to check CA private key"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: valid.CACertificate,
			CAKey: valid.CAKey, Certificate: valid.Certificate, Key: valid.Key,
			ClientCertificate: "x"}}, "failed to check client certificate"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: "x",
			Certificate: valid.Certificate, Key: valid.Key,
			ClientCertificate: valid.ClientCertificate, ClientKey: valid.ClientKey}},
			"failed to check CA certificate: PEM certificate does not exist"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: valid.CACertificate,
			Certificate: valid.Certificate, Key: valid.ClientKey,
			ClientCertificate: valid.ClientCertificate, ClientKey: valid.ClientKey}},
			"failed to check server certificate: failed to match certificate and private key"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: other.CACertificate,
			Certificate: valid.Certificate, Key: valid.Key,
			ClientCertificate: valid.ClientCertificate, ClientKey: valid.ClientKey}},
			"failed to check server certificate: failed to check signature of 'proximity-server'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: valid.CACertificate,
			Certificate: valid.Certificate, Key: valid.Key,
			ClientCertificate: other.ClientCertificate, ClientKey: other.ClientKey}},
			"failed to check client certificate: failed to check signature of 'proximity-client'"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: valid.CACertificate,
			CAKey: other.CAKey, Certificate: valid.Certificate, Key: valid.Key,
			ClientCertificate: valid.ClientCertificate, ClientKey: valid.ClientKey}},
			"failed to check CA: failed to match certificate and private key"},
		{Inbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNServer{CACertificate: valid.CACertificate,
			Certificate: valid.Certificate, Key: valid.Key, TLSCrypt: "key",
			ClientCertificate: valid.ClientCertificate, ClientKey: valid.ClientKey}},
			"invalid OpenVPN static key"},
	} {
		config := base.Clone()
		_, err := config.AddInbound(item.inbound, now)
		require.ErrorContains(t, err, item.message, item.message)
		require.Len(t, config.Inbounds, 1)
	}
}

func TestAddOutbound_Defaults(t *testing.T) {
	config := makeConfig(t)
	ids, slots := []string{}, []int{}
	for _, outbound := range config.Outbounds {
		ids, slots = append(ids, outbound.ID), append(slots, outbound.Slot)
	}
	require.Equal(t, []string{"HOST 1", "SOCKS5 1", "HTTP 1", "OpenVPN 1", "AmneziaWG 1"}, ids)
	require.Equal(t, []int{1, 2, 3, 4, 5}, slots)
	require.Equal(t, HostDefault, config.Outbounds[0].Host.Mode)
	require.Nil(t, config.Outbounds[0].Monitoring)
	require.Equal(t, Monitoring{Interval: 30, Timeout: 5, Retries: 3, Target: defaultProbeTarget},
		*config.Outbounds[3].Monitoring)
	require.Equal(t, [2]int{3, 10}, [2]int{config.Outbounds[3].Attempts,
		config.Outbounds[3].ReconnectTimeout})
	for _, outbound := range config.Outbounds {
		require.Equal(t, &Recovery{Enabled: true, Delay: 60}, outbound.Recovery)
	}
	require.Equal(t, OTPChallenge, config.Outbounds[3].OpenVPN.OTPMode)
	require.True(t, config.Outbounds[2].IsSplit())
	require.False(t, config.Outbounds[1].IsSplit())
	require.True(t, config.Outbounds[4].IsTunnel())
	{
		require.NoError(t, config.RemoveOutbound("SOCKS5 1"))
		outbound, err := config.AddOutbound(Outbound{Type: TypeHost, ID: " Office uplink ",
			Autostart: true, Socks5: &Socks5Options{}})
		require.NoError(t, err)
		require.Equal(t, "Office uplink", outbound.ID)
		require.True(t, outbound.Autostart)
		require.Equal(t, 2, outbound.Slot)
		require.Nil(t, outbound.Socks5)
		outbound, err = config.AddOutbound(Outbound{Type: TypeHost,
			Recovery: &Recovery{Attempts: 5}})
		require.NoError(t, err)
		require.Equal(t, &Recovery{Delay: 60, Attempts: 5}, outbound.Recovery)
		require.NoError(t, config.RemoveOutbound(outbound.ID))
		require.Equal(t, "HOST 2", config.NextID(TypeLabel(TypeHost)))
	}
}

func TestAddOutbound_Invalid(t *testing.T) {
	config := makeConfig(t)
	for _, item := range []struct {
		outbound Outbound
		message  string
	}{
		{Outbound{Type: "wireguard"}, "unexpected outbound type 'wireguard'"},
		{Outbound{Type: TypeHost, ID: "HOST 1"}, "ID 'HOST 1' already exists"},
		{Outbound{Type: TypeHost, ID: "a,b"}, "invalid ID 'a,b'"},
		{Outbound{Type: TypeHost, ID: "direct"}, "reserved ID 'direct'"},
		{Outbound{Type: TypeHost, Host: &HostOptions{Mode: "auto"}}, "unexpected host mode 'auto'"},
		{Outbound{Type: TypeHost, Host: &HostOptions{Mode: HostInterface, Interface: "bad name"}},
			"invalid interface name 'bad name'"},
		{Outbound{Type: TypeHost, Host: &HostOptions{Mode: HostInterface, Interface: "eth0",
			Gateway: "gw"}}, "cannot parse \"gw\" as gateway"},
		{Outbound{Type: TypeSocks5, Socks5: &Socks5Options{Server: "bad host", Port: 1}},
			"invalid server address 'bad host'"},
		{Outbound{Type: TypeSocks5, Socks5: &Socks5Options{Server: "::1", Port: 0}},
			"invalid port (0)"},
		{Outbound{Type: TypeHTTP, HTTP: &HTTPOptions{Server: "a", Port: 70000}},
			"invalid port (70000)"},
		{Outbound{Type: TypeHTTP, HTTP: &HTTPOptions{Server: "a", Port: 1, HTTPSServer: "b"}},
			"failed to check HTTPS proxy"},
		{Outbound{Type: TypeHTTP, HTTP: &HTTPOptions{Server: "a", Port: 1, FTPServer: "b"}},
			"failed to check FTP proxy"},
		{Outbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: "client\n"}},
			"OpenVPN configuration has no 'remote' directive"},
		{Outbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN,
			OTPMode: "sms"}}, "unexpected OTP mode 'sms'"},
		{Outbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN,
			OTPKey: "not base32 1"}}, "failed to decode OTP key"},
		{Outbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 1}}, "invalid monitoring interval (1), expected 5..3600"},
		{Outbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 5}}, "invalid monitoring timeout (0)"},
		{Outbound{Type: TypeOpenVPN, OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &Monitoring{Interval: 5, Timeout: 1}}, "invalid monitoring retries (0)"},
		{Outbound{Type: TypeHost, Attempts: 101}, "invalid connection attempts (101)"},
		{Outbound{Type: TypeHost, ReconnectTimeout: 3601}, "invalid reconnection timeout (3601)"},
		{Outbound{Type: TypeHost, Recovery: &Recovery{Enabled: true, Delay: 86401}},
			"invalid recovery delay (86401), expected 1..86400"},
		{Outbound{Type: TypeHost, Recovery: &Recovery{Delay: -1}}, "invalid recovery delay (-1)"},
		{Outbound{Type: TypeHost, Recovery: &Recovery{Attempts: 1001}},
			"invalid recovery attempts (1001), expected 0..1000"},
		{Outbound{Type: TypeHost, Recovery: &Recovery{Attempts: -1}},
			"invalid recovery attempts (-1)"},
		{Outbound{Type: TypeAmneziaWG, AmneziaWG: &AmneziaWGOptions{Config: sampleTunnel},
			Monitoring: &Monitoring{Interval: 5, Timeout: 1, Retries: 1, Target: "nohost"}},
			"failed to check monitoring target"},
		{Outbound{Type: TypeAmneziaWG}, "failed to check AmneziaWG configuration"},
	} {
		_, err := config.AddOutbound(item.outbound)
		require.ErrorContains(t, err, item.message)
	}
	require.Len(t, config.Outbounds, 5)
}

func TestAddRoute_Identifiers(t *testing.T) {
	config := makeConfig(t)
	first, err := config.AddRoute(Route{Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"a.test"}})
	require.NoError(t, err)
	require.Equal(t, "Route 1", first.ID)
	second, err := config.AddRoute(Route{ID: " Route 3 ", Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"b.test"}})
	require.NoError(t, err)
	require.Equal(t, "Route 3", second.ID)
	third, err := config.AddRoute(Route{Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"c.test"}})
	require.NoError(t, err)
	require.Equal(t, "Route 2", third.ID)
	require.Equal(t, "Route 4", config.NextRouteID())
	_, err = config.AddRoute(Route{ID: "Route 1", Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"d.test"}})
	require.EqualError(t, err, "route ID 'Route 1' already exists")
	{
		second.ID = "Route 1"
		require.EqualError(t, config.UpdateRoute(second.Key, second),
			"route ID 'Route 1' already exists")
		second.ID = ""
		require.NoError(t, config.UpdateRoute(second.Key, second))
		require.Equal(t, "Route 3", config.Routes[1].ID)
		second.ID = "Work sites"
		require.NoError(t, config.UpdateRoute(second.Key, second))
		require.Equal(t, "Work sites", config.Routes[1].ID)
		require.NoError(t, config.Validate())
	}
	{
		config.Routes[2].ID = "Work sites"
		require.EqualError(t, config.Validate(), "duplicate route ID 'Work sites'")
		config.Routes[2].ID = ""
		require.ErrorContains(t, config.Validate(), "invalid route ID length (0)")
		_, err := config.SetRoutes("OpenVPN 1", true, true, []string{"10.0.0.0/8"})
		require.NoError(t, err)
		config.Routes[3].ID = "Routes of AmneziaWG 1"
		_, err = config.SetRoutes("AmneziaWG 1", true, true, []string{"10.1.0.0/16"})
		require.NoError(t, err)
		require.Equal(t, "Routes of OpenVPN 1", config.Routes[1].ID)
		require.Equal(t, "Route 2", config.Routes[0].ID)
		require.Equal(t, "AmneziaWG 1", config.Routes[0].Provided)
	}
}

func TestAddRoute_Normalizes(t *testing.T) {
	config := makeConfig(t)
	{
		route, err := config.AddRoute(Route{Key: "ignored", ID: " corp ", Provided: "x",
			Directive: "IP-CIDR", Target: "SOCKS5 1", Enabled: true,
			Params: []string{" 10.1.2.3 ", "", "8.8.8.8:53"}})
		require.NoError(t, err)
		require.Len(t, route.Key, 12)
		require.Equal(t, Route{Key: route.Key, ID: "corp", Directive: "IP-CIDR",
			Target: "SOCKS5 1", Enabled: true, Params: []string{"10.1.2.3/32", "8.8.8.8/32:53"}},
			route)
	}
	{
		route, err := config.AddRoute(Route{Directive: "IP-CIDR", Target: "SOCKS5 1",
			Params: []string{"  # office ", " 10.1.2.3 ", "", "\t#dns", "8.8.8.8:53"}})
		require.NoError(t, err)
		require.Equal(t, []string{"# office", "10.1.2.3/32", "#dns", "8.8.8.8/32:53"}, route.Params)
		require.NoError(t, config.Validate())
		require.NoError(t, config.RemoveRoute(route.Key))
		_, err = config.AddRoute(Route{Directive: "DOMAIN", Target: "DIRECT",
			Params: []string{"# only comment"}})
		require.ErrorContains(t, err, "rule parameters do not exist")
	}
	for _, item := range []struct {
		route   Route
		message string
	}{
		{Route{Directive: "DOMAIN", Target: "missing", Params: []string{"a.test"}},
			"target 'missing' does not exist"},
		{Route{Directive: "DOMAIN", Target: "DIRECT", Params: []string{"a b"}},
			"failed to check rule parameters"},
		{Route{Directive: "DOMAIN", Target: "DIRECT"}, "rule parameters do not exist"},
		{Route{Directive: "NOPE", Target: "DIRECT", Params: []string{"x"}}, "unexpected directive"},
		{Route{Directive: "DOMAIN", Target: "DIRECT", Params: []string{"a.test"},
			ID: string(make([]byte, 65))}, "route ID is too long"},
	} {
		_, err := config.AddRoute(item.route)
		require.ErrorContains(t, err, item.message)
	}
	{
		key := config.Routes[0].Key
		require.NoError(t, config.UpdateRoute(key, Route{Directive: "DOMAIN-SUFFIX",
			Target: "REJECT", Params: []string{"*.Example.com"}}))
		require.Equal(t, []string{"example.com"}, config.Routes[0].Params)
		require.Equal(t, key, config.Routes[0].Key)
		require.ErrorContains(t, config.UpdateRoute(key, Route{}), "target '' does not exist")
		require.ErrorContains(t, config.UpdateRoute("missing", Route{}), "does not exist")
		_, err := config.AddRoute(Route{Directive: "GEOIP", Target: "DIRECT", Params: []string{"ru"}})
		require.NoError(t, err)
		require.NoError(t, config.MoveRoute(key, 1))
		require.Equal(t, key, config.Routes[1].Key)
		require.NoError(t, config.MoveRoute(key, 1))
		require.Equal(t, key, config.Routes[1].Key)
		require.ErrorContains(t, config.MoveRoute(key, 2), "invalid move direction (2)")
		require.ErrorContains(t, config.MoveRoute("missing", 1), "item does not exist")
		require.NoError(t, config.RemoveRoute(key))
		require.ErrorContains(t, config.RemoveRoute(key), "does not exist")
		require.NoError(t, config.Validate())
	}
}

func TestClone_Independent(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Outbounds: []string{"HOST 1"}})
	require.NoError(t, err)
	_, err = config.AddRoute(Route{Directive: "DOMAIN", Target: "DIRECT",
		Params: []string{"a.test"}})
	require.NoError(t, err)
	clone := config.Clone()
	require.Equal(t, *config, clone)
	clone.Outbounds[1].Socks5.Port, clone.Outbounds[2].HTTP.Port = 1, 1
	clone.Outbounds[0].Host.Mode, clone.Outbounds[3].OpenVPN.Config = "x", "x"
	clone.Outbounds[4].AmneziaWG.Config, clone.Outbounds[4].Monitoring.Interval = "x", 1
	clone.Outbounds[4].Recovery.Delay = 1
	clone.Fallbacks[0].Outbounds[0], clone.Routes[0].Params[0] = "x", "x"
	require.Equal(t, 1080, config.Outbounds[1].Socks5.Port)
	require.Equal(t, 3128, config.Outbounds[2].HTTP.Port)
	require.Equal(t, HostDefault, config.Outbounds[0].Host.Mode)
	require.Equal(t, sampleOpenVPN, config.Outbounds[3].OpenVPN.Config)
	require.Equal(t, sampleTunnel, config.Outbounds[4].AmneziaWG.Config)
	require.Equal(t, 30, config.Outbounds[4].Monitoring.Interval)
	require.Equal(t, 60, config.Outbounds[4].Recovery.Delay)
	require.Equal(t, "HOST 1", config.Fallbacks[0].Outbounds[0])
	require.Equal(t, "a.test", config.Routes[0].Params[0])
}

func TestGenerateMaterial_Kinds(t *testing.T) {
	now := time.Unix(1700000000, 0)
	full, err := GenerateMaterial(MaterialAll, Material{}, now)
	require.NoError(t, err)
	require.NoError(t, checkPair(full.CACertificate, full.CAKey))
	require.NoError(t, checkPair(full.Certificate, full.Key))
	require.NoError(t, checkPair(full.ClientCertificate, full.ClientKey))
	require.NoError(t, checkIssued(full.Certificate, full.CACertificate))
	require.NoError(t, checkIssued(full.ClientCertificate, full.CACertificate))
	require.NoError(t, checkStaticKey(full.TLSCrypt))
	require.Regexp(t, `^[A-Z2-7]{32}$`, full.OTPKey)
	authority, err := parseCertificate(full.CACertificate)
	require.NoError(t, err)
	require.True(t, authority.IsCA)
	require.Equal(t, now.Add(certificateLifetime).Unix(), authority.NotAfter.Unix())
	leaf, err := parseCertificate(full.Certificate)
	require.NoError(t, err)
	require.False(t, leaf.IsCA)
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
	{
		chain, err := GenerateMaterial(MaterialPKI, Material{}, now)
		require.NoError(t, err)
		require.Empty(t, chain.TLSCrypt+chain.OTPKey)
		require.NotEqual(t, full.CACertificate, chain.CACertificate)
		server, err := GenerateMaterial(MaterialServer, full, now)
		require.NoError(t, err)
		require.NoError(t, checkIssued(server.Certificate, full.CACertificate))
		require.Error(t, checkIssued(server.Certificate, chain.CACertificate))
		require.Empty(t, server.ClientCertificate+server.CACertificate)
		client, err := GenerateMaterial(MaterialClient, full, now)
		require.NoError(t, err)
		require.NoError(t, checkPair(client.ClientCertificate, client.ClientKey))
		parsed, err := parseCertificate(client.ClientCertificate)
		require.NoError(t, err)
		require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, parsed.ExtKeyUsage)
	}
	{
		static, err := GenerateMaterial(MaterialTLSCrypt, Material{}, now)
		require.NoError(t, err)
		require.NotEqual(t, full.TLSCrypt, static.TLSCrypt)
		code, err := GenerateMaterial(MaterialOTP, Material{}, now)
		require.NoError(t, err)
		require.NotEqual(t, full.OTPKey, code.OTPKey)
		single, err := GenerateMaterial(MaterialTLS, Material{}, now)
		require.NoError(t, err)
		require.NoError(t, checkPair(single.Certificate, single.Key))
		require.Equal(t, "proximity-server", CommonName(single.Certificate))
		require.Empty(t, single.CACertificate)
	}
	{
		_, err := GenerateMaterial("other", Material{}, now)
		require.EqualError(t, err, "unexpected material kind 'other'")
		_, err = GenerateMaterial(MaterialServer, Material{}, now)
		require.EqualError(t, err, "failed to check CA certificate: PEM certificate does not exist")
		_, err = GenerateMaterial(MaterialClient, Material{CACertificate: full.CACertificate}, now)
		require.EqualError(t, err, "failed to check CA private key: PEM private key does not exist")
		_, err = GenerateMaterial(MaterialClient, Material{CACertificate: full.CACertificate,
			CAKey: full.Key}, now)
		require.ErrorContains(t, err, "failed to check CA: failed to match certificate")
		forged, err := GenerateMaterial(MaterialServer, Material{CACertificate: full.Certificate,
			CAKey: full.Key}, now)
		require.NoError(t, err)
		require.ErrorContains(t, checkIssued(forged.Certificate, full.Certificate),
			"failed to check signature of 'proximity-server'")
	}
	{
		require.Empty(t, CommonName("broken"))
		require.Empty(t, normalizePEM(" \r\n "))
		require.Equal(t, "a\nb\n", normalizePEM("a\r\nb"))
		_, err := parseCertificate("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
		require.ErrorContains(t, err, "failed to parse certificate")
		_, err = parseKey("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n")
		require.EqualError(t, err, "unexpected private key format")
		require.EqualError(t, checkIssued("x", full.CACertificate),
			"PEM certificate does not exist")
		require.ErrorContains(t, checkIssued(full.Certificate, "x"),
			"failed to check CA certificate")
		block, _ := pem.Decode([]byte(full.Key))
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		require.NoError(t, err)
		encoded, err := x509.MarshalECPrivateKey(key.(*ecdsa.PrivateKey))
		require.NoError(t, err)
		_, err = parseKey(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY",
			Bytes: encoded})))
		require.NoError(t, err)
		legacy, err := rsa.GenerateKey(rand.Reader, 1024)
		require.NoError(t, err)
		_, err = parseKey(string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(legacy)})))
		require.NoError(t, err)
	}
}

func TestImport_Backup(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Enabled: true, Outbounds: []string{"HOST 1"}})
	require.NoError(t, err)
	_, err = config.AddRoute(Route{Directive: "DOMAIN", Target: "Fallback 1", Enabled: true,
		Params: []string{"a.test"}})
	require.NoError(t, err)
	config.Settings.DefaultTarget, config.Settings.MixedPort = "Fallback 1", 7000
	require.NoError(t, config.AddUser(User{Name: "admin", Role: RoleAdministrator,
		PasswordHash: "h"}))
	backup := config.Export(time.Unix(1700000000, 0))
	require.Equal(t, BackupFormat, backup.Format)
	require.Equal(t, currentVersion, backup.Version)
	require.Equal(t, "2023-11-14T22:13:20Z", backup.Created)
	require.Len(t, backup.Outbounds, 5)
	data, err := json.Marshal(backup)
	require.NoError(t, err)
	require.NotContains(t, string(data), "password_hash")
	{
		target := Default()
		target.Users, target.Secret = config.Users, "secret"
		target.Settings.LogLevel = "debug"
		require.NoError(t, target.Import(backup, false))
		require.NoError(t, target.Validate())
		require.Equal(t, config.Outbounds, target.Outbounds)
		require.Equal(t, config.Fallbacks, target.Fallbacks)
		require.Equal(t, config.Routes, target.Routes)
		require.Equal(t, 7899, target.Settings.MixedPort)
		require.Equal(t, "debug", target.Settings.LogLevel)
		require.Equal(t, "Fallback 1", target.Settings.DefaultTarget)
		require.Equal(t, "secret", target.Secret)
		require.Len(t, target.Users, 1)
		statistics := backup.Outbounds[0].Statistics
		backup.Outbounds[0].Statistics = !statistics
		require.Equal(t, statistics, target.Outbounds[0].Statistics)
		backup.Outbounds[0].Statistics = statistics
	}
	{
		target := Default()
		require.NoError(t, target.Import(backup, true))
		require.NoError(t, target.Validate())
		require.Equal(t, 7000, target.Settings.MixedPort)
	}
	{
		legacy := Backup{Format: BackupFormat, Version: 1, Settings: Default().Settings,
			Fallbacks: []Fallback{{ID: "Old", Enabled: true, Legacy: []string{"HOST 1"}}},
			Legacy: Legacy{Endpoints: backup.Outbounds[:1], Filters: []Route{{Directive: "DOMAIN",
				Target: "Old", Params: []string{"a.test"}}, {Directive: "DOMAIN", Target: "Old",
				Params: []string{"b.test"}}}}}
		legacy.Settings.ConnectivityChecks, legacy.Settings.Capture = false, false
		target := Default()
		require.NoError(t, target.Import(legacy, true))
		require.NoError(t, target.Validate())
		require.True(t, target.Fallbacks[0].HealthCheck)
		require.Equal(t, []string{"HOST 1"}, target.Fallbacks[0].Outbounds)
		require.Nil(t, target.Fallbacks[0].Legacy)
		require.Equal(t, "HOST 1", target.Outbounds[0].ID)
		require.Equal(t, Legacy{}, target.Legacy)
		require.True(t, target.Settings.ConnectivityChecks)
		require.True(t, target.Settings.Capture)
		require.Equal(t, "Route 1", target.Routes[0].ID)
		require.Equal(t, "Route 2", target.Routes[1].ID)
		require.Len(t, target.Routes[0].Key, 12)
		require.NotEqual(t, target.Routes[0].Key, target.Routes[1].Key)
	}
	{
		target := Default()
		require.NoError(t, target.Import(Backup{Format: BackupFormat, Version: currentVersion,
			Settings: Default().Settings}, true))
		require.NotNil(t, target.Outbounds)
		require.NotNil(t, target.Fallbacks)
		require.NotNil(t, target.Routes)
		require.EqualError(t, target.Import(Backup{Format: "other", Version: 1}, true),
			"unexpected backup format 'other'")
		require.EqualError(t, target.Import(Backup{Format: BackupFormat, Version: 99}, true),
			"unexpected backup version (99)")
		require.EqualError(t, target.Import(Backup{Format: BackupFormat}, true),
			"unexpected backup version (0)")
	}
	{
		_, err := config.AddInbound(Inbound{Type: TypeSocks5, Anonymous: true}, time.Now())
		require.NoError(t, err)
		current := config.Export(time.Now())
		require.Len(t, current.Inbounds, 1)
		target := Default()
		_, err = target.AddInbound(Inbound{Type: TypeSocks5, ID: "Kept", Anonymous: true},
			time.Now())
		require.NoError(t, err)
		previous := current
		previous.Version, previous.Inbounds = inboundVersion-1, nil
		require.NoError(t, target.Import(previous, true))
		require.NoError(t, target.Validate())
		require.Equal(t, "Kept", target.Inbounds[0].ID)
		require.NoError(t, target.Import(current, true))
		require.NoError(t, target.Validate())
		require.Equal(t, config.Inbounds, target.Inbounds)
		current.Inbounds = nil
		require.NoError(t, target.Import(current, true))
		require.Equal(t, []Inbound{}, target.Inbounds)
	}
	{
		require.True(t, Default().Settings.Autostart)
		current := config.Export(time.Now())
		current.Settings.Autostart = false
		target := Default()
		require.NoError(t, target.Import(current, true))
		require.False(t, target.Settings.Autostart)
		previous := current
		previous.Version = autostartVersion - 1
		require.NoError(t, target.Import(previous, false))
		require.False(t, target.Settings.Autostart)
		require.NoError(t, target.Import(previous, true))
		require.True(t, target.Settings.Autostart)
	}
}

func TestInboundProblem_Reasons(t *testing.T) {
	config := makeConfig(t)
	users := []InboundUser{{Enabled: true, Name: "a", Password: "x"}}
	require.Empty(t, config.InboundProblem(Inbound{Outbound: ViaRouter, Anonymous: true}))
	require.Empty(t, config.InboundProblem(Inbound{Outbound: ViaHost, Users: users}))
	require.Empty(t, config.InboundProblem(Inbound{Outbound: "HOST 1", Users: users}))
	require.Equal(t, "outbound 'gone' does not exist",
		config.InboundProblem(Inbound{Outbound: "gone", Anonymous: true}))
	require.Equal(t, "outbound 'AmneziaWG 1' is disabled",
		config.InboundProblem(Inbound{Outbound: "AmneziaWG 1", Anonymous: true}))
	require.Equal(t, "anonymous connections are not allowed and enabled users do not exist",
		config.InboundProblem(Inbound{Outbound: ViaRouter, Users: []InboundUser{{Name: "a"}}}))
}

func TestMoveOutbound_Order(t *testing.T) {
	config := makeConfig(t)
	require.NoError(t, config.MoveOutbound("HOST 1", -1))
	require.Equal(t, "HOST 1", config.Outbounds[0].ID)
	require.NoError(t, config.MoveOutbound("HOST 1", 1))
	require.Equal(t, "SOCKS5 1", config.Outbounds[0].ID)
	require.Equal(t, "HOST 1", config.Outbounds[1].ID)
	require.ErrorContains(t, config.MoveOutbound("missing", 1), "item does not exist")
}

func TestOpen_CreatesAndReloads(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "nested")
	store, err := Open(directory)
	require.NoError(t, err)
	secret := store.Snapshot().Secret
	require.Len(t, secret, 44)
	calls := 0
	store.Subscribe(func() {
		calls++
	})
	require.NoError(t, store.Update(func(config *Config) error {
		_, err := config.AddOutbound(Outbound{Type: TypeHost, Enabled: true})
		return err
	}))
	require.Equal(t, 1, calls)
	{
		failure := errors.New("rejected")
		require.ErrorIs(t, store.Update(func(config *Config) error {
			config.Outbounds = nil
			return failure
		}), failure)
		require.ErrorContains(t, store.Update(func(config *Config) error {
			config.Settings.MixedPort = 0
			return nil
		}), "invalid proxy port (0)")
		require.Equal(t, 1, calls)
		require.Len(t, store.Snapshot().Outbounds, 1)
		require.Equal(t, 7899, store.Snapshot().Settings.MixedPort)
	}
	{
		reloaded, err := Open(directory)
		require.NoError(t, err)
		require.Equal(t, store.Snapshot(), reloaded.Snapshot())
		information, err := os.Stat(filepath.Join(directory, fileName))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), information.Mode().Perm())
	}
	{
		_, err := Open(filepath.Join(directory, fileName, "sub"))
		require.ErrorContains(t, err, "failed to create directory")
		require.NoError(t, os.Mkdir(filepath.Join(directory, "folder"), 0o700))
		require.NoError(t, os.Mkdir(filepath.Join(directory, "folder", fileName), 0o700))
		_, err = Open(filepath.Join(directory, "folder"))
		require.ErrorContains(t, err, "config.Open failed: failed to read file")
	}
}

func TestOpen_Migrates(t *testing.T) {
	directory := t.TempDir()
	legacy := `{"version":1,"secret":"s","settings":{"mixed_port":7890,"bind_address":"*",` +
		`"default_target":"DIRECT","log_level":"warning","probe_target":"1.1.1.1:443",` +
		`"check_interval":30},"users":[],"endpoints":[{"id":"HOST 1","type":"host","slot":1,` +
		`"enabled":true,"host":{"mode":"default"}}],"fallbacks":[{"id":"Fallback 1",` +
		`"enabled":true,"endpoints":["HOST 1"],"url":"https://example.com/","interval":60}],` +
		`"filters":[{"key":"aaaaaaaaaaaa","id":"","directive":"DOMAIN","target":"DIRECT",` +
		`"params":["a.test"],"enabled":true},{"key":"bbbbbbbbbbbb","id":"same",` +
		`"directive":"DOMAIN","target":"DIRECT","params":["b.test"]},{"key":"cccccccccccc",` +
		`"id":"same","directive":"DOMAIN","target":"DIRECT","params":["c.test"]}]}`
	require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte(legacy), 0o600))
	store, err := Open(directory)
	require.NoError(t, err)
	snapshot := store.Snapshot()
	require.Equal(t, currentVersion, snapshot.Version)
	require.Equal(t, 7890, snapshot.Settings.MixedPort)
	require.True(t, snapshot.Settings.ConnectivityChecks)
	require.True(t, snapshot.Settings.Capture)
	require.True(t, snapshot.Fallbacks[0].HealthCheck)
	require.Equal(t, []string{"HOST 1"}, snapshot.Fallbacks[0].Outbounds)
	require.Equal(t, &HostOptions{Mode: HostDefault}, snapshot.Outbounds[0].Host)
	require.Equal(t, 3, snapshot.Outbounds[0].Attempts)
	require.Equal(t, &Recovery{Enabled: true, Delay: 60}, snapshot.Outbounds[0].Recovery)
	require.Equal(t, "Route 1", snapshot.Routes[0].ID)
	require.Equal(t, "same", snapshot.Routes[1].ID)
	require.Equal(t, "Route 2", snapshot.Routes[2].ID)
	require.True(t, snapshot.Settings.Autostart)
	require.True(t, snapshot.Outbounds[0].Autostart)
	{
		previous := `{"version":6,"secret":"s","settings":{"mixed_port":7890,"bind_address":"*",` +
			`"default_target":"DIRECT","log_level":"warning","probe_target":"1.1.1.1:443",` +
			`"check_interval":30,"autostart":true},"users":[],"outbounds":[{"id":"HOST 1",` +
			`"type":"host","slot":1,"enabled":true,"host":{"mode":"default","attempts":5,` +
			`"reconnect_timeout":7}},{"id":"AWG","type":"amneziawg","slot":2,"enabled":true,` +
			`"amneziawg":{"config":` + strconv.Quote(sampleTunnel) + `},"monitoring":{"interval":30,` +
			`"timeout":5,"retries":3,"reconnect_delay":4,"reconnect_attempts":0,` +
			`"target":"1.1.1.1:443"}}],"inbounds":[{"id":"S","type":"socks5","port":7800,` +
			`"anonymous":true}]}`
		require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte(previous),
			0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		snapshot := store.Snapshot()
		require.Equal(t, [2]int{5, 7}, [2]int{snapshot.Outbounds[0].Attempts,
			snapshot.Outbounds[0].ReconnectTimeout})
		require.Equal(t, [2]int{1, 4}, [2]int{snapshot.Outbounds[1].Attempts,
			snapshot.Outbounds[1].ReconnectTimeout})
		require.Equal(t, [2]int{3, 10}, [2]int{snapshot.Inbounds[0].Attempts,
			snapshot.Inbounds[0].ReconnectTimeout})
		require.Equal(t, &Recovery{Enabled: true, Delay: 60}, snapshot.Inbounds[0].Recovery)
		require.NoError(t, store.Update(func(*Config) error {
			return nil
		}))
		data, err := os.ReadFile(filepath.Join(directory, fileName))
		require.NoError(t, err)
		require.NotContains(t, string(data), "reconnect_delay")
		require.NotContains(t, string(data), "reconnect_attempts")
	}
	{
		current := `{"version":6,"secret":"s","settings":{"mixed_port":7890,"bind_address":"*",` +
			`"default_target":"DIRECT","log_level":"warning","probe_target":"1.1.1.1:443",` +
			`"check_interval":30,"autostart":true},"users":[],"outbounds":[{"id":"HOST 1",` +
			`"type":"host","slot":1,"enabled":true,"autostart":false,"name":"old",` +
			`"group":"old","host":{"mode":"default"}}]}`
		require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte(current),
			0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		require.False(t, store.Snapshot().Outbounds[0].Autostart)
		require.NoError(t, store.Update(func(*Config) error {
			return nil
		}))
		data, err := os.ReadFile(filepath.Join(directory, fileName))
		require.NoError(t, err)
		require.NotContains(t, string(data), `"group"`)
	}
	{
		current := `{"version":5,"secret":"s","settings":{"mixed_port":7890,"bind_address":"*",` +
			`"default_target":"DIRECT","log_level":"warning","probe_target":"1.1.1.1:443",` +
			`"check_interval":30,"autostart":false},"users":[]}`
		require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte(current),
			0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		require.False(t, store.Snapshot().Settings.Autostart)
		require.True(t, store.Snapshot().Settings.PingFilter)
	}
	{
		current := `{"version":9,"secret":"s","settings":{"mixed_port":7890,"bind_address":"*",` +
			`"default_target":"DIRECT","log_level":"warning","probe_target":"1.1.1.1:443",` +
			`"check_interval":30,"ping_filter":false},"users":[]}`
		require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), []byte(current),
			0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		require.False(t, store.Snapshot().Settings.PingFilter)
		require.True(t, Default().Settings.PingFilter)
	}
}

func TestOpen_Repairs(t *testing.T) {
	directory := t.TempDir()
	path, report := filepath.Join(directory, fileName), filepath.Join(directory, problemsName)
	{
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		problems := store.Problems()
		require.Equal(t, []Problem{{Kind: KindFile, ID: fileName,
			Reason: "unexpected end of JSON input"}}, problems.Items)
		require.Regexp(t, `^proximity\.invalid-\d+\.json$`, problems.Backup)
		data, err := os.ReadFile(filepath.Join(directory, problems.Backup))
		require.NoError(t, err)
		require.Equal(t, "{", string(data))
		snapshot := store.Snapshot()
		require.NotEmpty(t, snapshot.Secret)
		require.Empty(t, snapshot.Outbounds)
		store, err = Open(directory)
		require.NoError(t, err)
		require.Equal(t, problems, store.Problems())
		require.Equal(t, snapshot.Secret, store.Snapshot().Secret)
		require.NoError(t, store.ClearProblems())
		require.Empty(t, store.Problems().Items)
		require.NoFileExists(t, report)
		require.NoError(t, store.ClearProblems())
		store, err = Open(directory)
		require.NoError(t, err)
		require.Equal(t, Problems{Items: []Problem{}}, store.Problems())
	}
	{
		require.NoError(t, os.WriteFile(path, []byte(`{"secret":7,"settings":{"log_level":"loud"},`+
			`"routes":5,"users":[{"role":1},7]}`), 0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		require.Equal(t, []Problem{
			{Kind: KindUser, ID: "#1", Reason: unmarshalError(t, `{"role":1}`, &User{})},
			{Kind: KindUser, ID: "#2", Reason: unmarshalError(t, `7`, &User{})},
			{Kind: KindRoute, ID: "*", Reason: "unexpected JSON number instead of list"},
			{Kind: KindSettings, ID: KindSettings, Reason: "failed to check settings: unexpected " +
				"log level 'loud'"}}, store.Problems().Items)
		require.Equal(t, Default().Settings, store.Snapshot().Settings)
		require.NotEmpty(t, store.Snapshot().Secret)
		require.NoError(t, os.WriteFile(path, []byte(`{"settings":7}`), 0o600))
		store, err = Open(directory)
		require.NoError(t, err)
		require.Len(t, store.Problems().Items, 5)
		require.Equal(t, Problem{Kind: KindSettings, ID: KindSettings,
			Reason: unmarshalError(t, `7`, &Settings{})}, store.Problems().Items[4])
		require.NoError(t, os.WriteFile(report, []byte("{"), 0o600))
		require.NoError(t, os.WriteFile(path, []byte(`{"version":7,"version":"x"}`), 0o600))
		store, err = Open(directory)
		require.NoError(t, err)
		require.Len(t, store.Problems().Items, 1)
		require.NoError(t, store.ClearProblems())
		require.NoError(t, os.WriteFile(path, []byte(`{"outbounds":{},"fallbacks":"x",`+
			`"inbounds":true,"routes":null}`), 0o600))
		store, err = Open(directory)
		require.NoError(t, err)
		require.Equal(t, []Problem{
			{Kind: KindOutbound, ID: "*", Reason: "unexpected JSON object instead of list"},
			{Kind: KindFallback, ID: "*", Reason: "unexpected JSON string instead of list"},
			{Kind: KindInbound, ID: "*", Reason: "unexpected JSON boolean instead of list"}},
			store.Problems().Items)
		require.NoError(t, store.ClearProblems())
	}
	{
		config := makeConfig(t)
		for _, members := range [][]string{{"SOCKS5 1", "HOST 1"}, {"OpenVPN 1"}} {
			_, err := config.AddFallback(Fallback{Enabled: true, Outbounds: members})
			require.NoError(t, err)
		}
		for _, target := range []string{"SOCKS5 1", "Fallback 1", "OpenVPN 1", "HOST 1"} {
			_, err := config.AddRoute(Route{Directive: "DOMAIN-SUFFIX", Target: target,
				Params: []string{"example.org"}, Enabled: true})
			require.NoError(t, err)
		}
		for _, outbound := range []string{ViaRouter, ViaRouter, "HTTP 1"} {
			_, err := config.AddInbound(Inbound{Type: TypeSocks5, Anonymous: true, Enabled: true,
				Outbound: outbound}, time.Now())
			require.NoError(t, err)
		}
		config.Users = []User{{Name: "admin", Role: RoleAdministrator, PasswordHash: "x"},
			{Name: "king", Role: "king", PasswordHash: "x"}}
		config.Settings.DefaultTarget, config.Secret = "SOCKS5 1", "c2VjcmV0"
		data, err := json.Marshal(config)
		require.NoError(t, err)
		fields := map[string]any{}
		require.NoError(t, json.Unmarshal(data, &fields))
		outbounds := fields["outbounds"].([]any)
		outbounds[1].(map[string]any)["socks5"].(map[string]any)["port"] = "abc"
		outbounds[2].(map[string]any)["http"].(map[string]any)["port"] = 0
		fields["outbounds"] = append(outbounds, outbounds[0])
		fields["routes"].([]any)[3].(map[string]any)["directive"] = "BOGUS"
		fields["inbounds"].([]any)[1].(map[string]any)["port"] = 1
		fields["version"] = "seven"
		data, err = json.Marshal(fields)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		store, err := Open(directory)
		require.NoError(t, err)
		require.Equal(t, []Problem{
			{Kind: KindSettings, ID: "version", Reason: unmarshalError(t, `"seven"`, new(int))},
			{Kind: KindOutbound, ID: "SOCKS5 1",
				Reason: unmarshalError(t, `{"socks5":{"port":"abc"}}`, &Outbound{})},
			{Kind: KindUser, ID: "king", Reason: "unexpected role 'king'"},
			{Kind: KindOutbound, ID: "HTTP 1", Reason: "failed to check outbound 'HTTP 1': " +
				"invalid port (0)"},
			{Kind: KindOutbound, ID: "HOST 1", Reason: "duplicate outbound ID 'HOST 1'"},
			{Kind: KindFallback, ID: "Fallback 1", Reason: "failed to check fallback " +
				"'Fallback 1': outbound 'SOCKS5 1' does not exist"},
			{Kind: KindRoute, ID: "Route 4", Reason: "failed to check route 4: failed to check " +
				"rule parameters: unexpected directive 'BOGUS'"},
			{Kind: KindInbound, ID: "SOCKS5 2", Reason: "failed to check inbound 'SOCKS5 2': " +
				"invalid port (1), expected 7800..7898"},
			{Kind: KindSettings, ID: "default target", Reason: "failed to check settings: " +
				"default target 'SOCKS5 1' does not exist"},
			{Kind: KindRoute, ID: "Route 1", Reason: "target 'SOCKS5 1' does not exist", Kept: true},
			{Kind: KindRoute, ID: "Route 2", Reason: "target 'Fallback 1' does not exist",
				Kept: true},
			{Kind: KindInbound, ID: "SOCKS5 3", Reason: "outbound 'HTTP 1' does not exist",
				Kept: true}}, store.Problems().Items)
		snapshot := store.Snapshot()
		ids := func(count int, nameFunc func(index int) string) []string {
			result := []string{}
			for index := range count {
				result = append(result, nameFunc(index))
			}
			return result
		}
		require.Equal(t, []string{"HOST 1", "OpenVPN 1", "AmneziaWG 1"}, ids(len(snapshot.Outbounds),
			func(index int) string {
				return snapshot.Outbounds[index].ID
			}))
		require.Equal(t, []string{"Fallback 2"}, ids(len(snapshot.Fallbacks), func(index int) string {
			return snapshot.Fallbacks[index].ID
		}))
		require.Equal(t, []string{"Route 1", "Route 2", "Route 3"}, ids(len(snapshot.Routes),
			func(index int) string {
				return snapshot.Routes[index].ID
			}))
		require.Equal(t, []string{"SOCKS5 1", "SOCKS5 3"}, ids(len(snapshot.Inbounds),
			func(index int) string {
				return snapshot.Inbounds[index].ID
			}))
		require.Equal(t, []string{"admin"}, ids(len(snapshot.Users), func(index int) string {
			return snapshot.Users[index].Name
		}))
		require.Equal(t, TargetDirect, snapshot.Settings.DefaultTarget)
		require.Equal(t, currentVersion, snapshot.Version)
		require.Equal(t, config.Secret, snapshot.Secret)
		saved, err := os.ReadFile(filepath.Join(directory, store.Problems().Backup))
		require.NoError(t, err)
		require.Equal(t, data, saved)
		store, err = Open(directory)
		require.NoError(t, err)
		require.Len(t, store.Problems().Items, 12)
		require.Equal(t, snapshot, store.Snapshot())
	}
	{
		require.NoError(t, os.Remove(report))
		require.NoError(t, os.Mkdir(report, 0o700))
		store, err := Open(directory)
		require.NoError(t, err)
		require.Empty(t, store.Problems().Items)
		require.NoError(t, os.WriteFile(filepath.Join(report, "file"), []byte("x"), 0o600))
		require.ErrorContains(t, store.ClearProblems(), "failed to remove file")
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
		_, err = Open(directory)
		require.ErrorContains(t, err, "config.Open failed: failed to repair file")
	}
}

func TestOpen_RoutesMigration(t *testing.T) {
	directory := t.TempDir()
	config := makeConfig(t)
	second, err := config.AddOutbound(Outbound{Type: TypeOpenVPN, Enabled: true,
		OpenVPN: &OpenVPNOptions{Config: sampleOpenVPN}})
	require.NoError(t, err)
	for _, id := range []string{"OpenVPN 1", second.ID, "AmneziaWG 1"} {
		_, err := config.SetRoutes(id, true, true, []string{"10.0.0.0/8"})
		require.NoError(t, err)
	}
	config.Routes[1].Enabled, config.Version, config.Secret = false, routesVersion-1, "c2VjcmV0"
	data, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, fileName), data, 0o600))
	store, err := Open(directory)
	require.NoError(t, err)
	snapshot := store.Snapshot()
	require.Equal(t, currentVersion, snapshot.Version)
	require.True(t, snapshot.Outbounds[3].OpenVPN.UseRoutes)
	require.False(t, snapshot.Outbounds[5].OpenVPN.UseRoutes)
	require.True(t, snapshot.Outbounds[4].AmneziaWG.UseRoutes)
	require.Empty(t, store.Problems().Items)
	{
		config.Secret = ""
		require.Equal(t, networkBase, config.networkBase())
		config.Secret = "c2VjcmV0"
		base := config.networkBase()
		require.GreaterOrEqual(t, base, 64)
		require.Less(t, base, 192)
		created, err := config.AddInbound(Inbound{Type: TypeOpenVPN, Outbound: ViaHost},
			time.Unix(1700000000, 0))
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("10.%v.1.0/24", base), created.OpenVPN.Network)
	}
}

func TestRemoveOutbound_Cascade(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Outbounds: []string{"SOCKS5 1", "OpenVPN 1"}})
	require.NoError(t, err)
	changed, err := config.SetRoutes("OpenVPN 1", true, true, []string{"10.0.0.0/8"})
	require.NoError(t, err)
	require.True(t, changed)
	config.Settings.DefaultTarget = "OpenVPN 1"
	require.NoError(t, config.RemoveOutbound("OpenVPN 1"))
	require.Equal(t, []string{"SOCKS5 1"}, config.Fallbacks[0].Outbounds)
	require.Empty(t, config.Routes)
	require.Equal(t, TargetDirect, config.Settings.DefaultTarget)
	require.ErrorContains(t, config.RemoveOutbound("OpenVPN 1"), "does not exist")
	require.NoError(t, config.Validate())
}

func TestSetRoutes_Lifecycle(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddRoute(Route{Directive: "DOMAIN", Target: "HOST 1", Enabled: true,
		Params: []string{"a.test"}})
	require.NoError(t, err)
	networks := []string{"10.0.0.0/8", "172.16.0.0/12"}
	{
		for _, item := range [][2]bool{{false, true}, {true, false}} {
			changed, err := config.SetRoutes("OpenVPN 1", item[0], item[1], networks)
			require.NoError(t, err)
			require.False(t, changed)
		}
		changed, err := config.SetRoutes("OpenVPN 1", true, true, nil)
		require.NoError(t, err)
		require.False(t, changed)
		require.Len(t, config.Routes, 1)
	}
	{
		changed, err := config.SetRoutes("OpenVPN 1", true, true, networks)
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, Route{Key: config.Routes[0].Key, ID: "Routes of OpenVPN 1",
			Directive: "IP-CIDR", Target: "OpenVPN 1", Provided: "OpenVPN 1", Enabled: true,
			Params: networks}, config.Routes[0])
		require.Equal(t, "Route 1", config.Routes[1].ID)
		require.NoError(t, config.Validate())
	}
	{
		key := config.Routes[0].Key
		require.EqualError(t, config.UpdateRoute(key, Route{Target: "REJECT",
			Params: []string{"1.1.1.1"}}), "provided route follows its VPN connection")
		require.ErrorContains(t, config.RemoveRoute(key), "provided route is read-only")
		require.NoError(t, config.MoveRoute(key, 1))
		require.Equal(t, "Routes of OpenVPN 1", config.Routes[1].ID)
	}
	{
		changed, err := config.SetRoutes("OpenVPN 1", true, true, networks)
		require.NoError(t, err)
		require.False(t, changed)
		changed, err = config.SetRoutes("OpenVPN 1", true, true, []string{"10.0.0.0/8"})
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, []string{"10.0.0.0/8"}, config.Routes[1].Params)
		changed, err = config.SetRoutes("OpenVPN 1", true, false, nil)
		require.NoError(t, err)
		require.True(t, changed)
		require.False(t, config.Routes[1].Enabled)
		require.Equal(t, []string{"10.0.0.0/8"}, config.Routes[1].Params)
		changed, err = config.SetRoutes("OpenVPN 1", true, true, []string{})
		require.NoError(t, err)
		require.False(t, changed)
		changed, err = config.SetRoutes("OpenVPN 1", true, true, networks)
		require.NoError(t, err)
		require.True(t, changed)
		require.True(t, config.Routes[1].Enabled)
		require.Equal(t, networks, config.Routes[1].Params)
		require.Equal(t, "Routes of OpenVPN 1", config.Routes[1].ID)
	}
	{
		changed, err := config.SetRoutes("OpenVPN 1", false, true, networks)
		require.NoError(t, err)
		require.True(t, changed)
		require.Len(t, config.Routes, 1)
		require.Equal(t, "Route 1", config.Routes[0].ID)
	}
	{
		require.False(t, config.Outbounds[0].UsesRoutes())
		config.Outbounds[3].OpenVPN.UseRoutes, config.Outbounds[4].AmneziaWG.UseRoutes = true, true
		require.True(t, config.Outbounds[3].UsesRoutes())
		require.True(t, config.Outbounds[4].UsesRoutes())
	}
}

func TestUpdateInbound_Identity(t *testing.T) {
	config := makeConfig(t)
	now := time.Unix(1700000000, 0)
	_, err := config.AddInbound(Inbound{Type: TypeSocks5, Outbound: "SOCKS5 1"}, now)
	require.NoError(t, err)
	created, err := config.AddInbound(Inbound{Type: TypeOpenVPN, Outbound: ViaHost}, now)
	require.NoError(t, err)
	updated, err := config.UpdateInbound("OpenVPN 1", Inbound{Type: TypeSocks5, ID: " Office ",
		Enabled: true, Outbound: "SOCKS5 1", OpenVPN: created.OpenVPN}, now)
	require.NoError(t, err)
	require.Equal(t, "Office", updated.ID)
	require.Equal(t, TypeOpenVPN, updated.Type)
	require.Equal(t, InboundPortFirst+1, updated.Port)
	require.Equal(t, created.OpenVPN.Certificate, updated.OpenVPN.Certificate)
	found, exists := config.FindInbound("Office")
	require.True(t, exists)
	require.Equal(t, updated, found)
	_, exists = config.FindInbound("OpenVPN 1")
	require.False(t, exists)
	{
		_, err := config.UpdateInbound("missing", Inbound{}, now)
		require.EqualError(t, err, "inbound 'missing' does not exist")
		_, err = config.UpdateInbound("Office", Inbound{ID: "SOCKS5 1"}, now)
		require.EqualError(t, err, "ID 'SOCKS5 1' already exists")
		_, err = config.UpdateInbound("Office", Inbound{Port: InboundPortFirst}, now)
		require.EqualError(t, err, "port (7800) is used by inbound 'SOCKS5 1'")
		kept, err := config.UpdateInbound("Office", Inbound{Port: 7850, OpenVPN: updated.OpenVPN},
			now)
		require.NoError(t, err)
		require.Equal(t, "Office", kept.ID)
		require.Equal(t, 7850, kept.Port)
		require.Equal(t, ViaRouter, kept.Outbound)
		require.Equal(t, "10.77.2.0/24", kept.OpenVPN.Network)
	}
	{
		renamed, _ := config.FindOutbound("SOCKS5 1")
		renamed.ID = "Upstream"
		require.NoError(t, config.UpdateOutbound("SOCKS5 1", renamed))
		require.Equal(t, "Upstream", config.Inbounds[0].Outbound)
		require.NoError(t, config.RemoveOutbound("Upstream"))
		require.Equal(t, "Upstream", config.Inbounds[0].Outbound)
		require.Equal(t, "outbound 'Upstream' does not exist",
			config.InboundProblem(config.Inbounds[0]))
		require.NoError(t, config.Validate())
	}
	{
		require.NoError(t, config.MoveInbound("Office", -1))
		require.Equal(t, "Office", config.Inbounds[0].ID)
		require.EqualError(t, config.MoveInbound("missing", 1), "item does not exist")
		require.EqualError(t, config.RemoveInbound("missing"), "inbound 'missing' does not exist")
		require.NoError(t, config.RemoveInbound("Office"))
		require.Len(t, config.Inbounds, 1)
		require.Equal(t, "OpenVPN 1", config.NextInboundID("OpenVPN"))
	}
}

func TestUpdateOutbound_Identity(t *testing.T) {
	config := makeConfig(t)
	require.NoError(t, config.UpdateOutbound("SOCKS5 1", Outbound{Type: TypeHost, Slot: 77,
		Autostart: true, Socks5: &Socks5Options{Server: "10.0.0.9", Port: 1081}}))
	outbound, found := config.FindOutbound("SOCKS5 1")
	require.True(t, found)
	require.Equal(t, Outbound{ID: "SOCKS5 1", Type: TypeSocks5, Slot: 2, Autostart: true,
		Attempts: 3, ReconnectTimeout: 10, Recovery: &Recovery{Enabled: true, Delay: 60},
		Socks5: &Socks5Options{Server: "10.0.0.9", Port: 1081}}, outbound)
	require.ErrorContains(t, config.UpdateOutbound("SOCKS5 1", Outbound{}), "invalid server address")
	require.ErrorContains(t, config.UpdateOutbound("missing", Outbound{}), "does not exist")
	_, found = config.FindOutbound("missing")
	require.False(t, found)
	{
		outbound.ID = "HOST 1"
		require.EqualError(t, config.UpdateOutbound("SOCKS5 1", outbound), "ID 'HOST 1' already exists")
		outbound.ID = "DIRECT"
		require.EqualError(t, config.UpdateOutbound("SOCKS5 1", outbound), "ID 'DIRECT' already exists")
		outbound.ID = "bad,id"
		require.EqualError(t, config.UpdateOutbound("SOCKS5 1", outbound), "invalid ID 'bad,id'")
		outbound.ID = " SOCKS5 1 "
		require.NoError(t, config.UpdateOutbound("SOCKS5 1", outbound))
	}
}

func TestUpdateOutbound_Rename(t *testing.T) {
	config := makeConfig(t)
	_, err := config.AddFallback(Fallback{Enabled: true, Outbounds: []string{"HOST 1",
		"OpenVPN 1"}})
	require.NoError(t, err)
	_, err = config.AddRoute(Route{Directive: "DOMAIN", Target: "OpenVPN 1",
		Params: []string{"a.test"}})
	require.NoError(t, err)
	_, err = config.AddRoute(Route{Directive: "DOMAIN", Target: "Fallback 1",
		Params: []string{"b.test"}})
	require.NoError(t, err)
	_, err = config.SetRoutes("OpenVPN 1", true, true, []string{"10.0.0.0/8"})
	require.NoError(t, err)
	config.Settings.DefaultTarget = "OpenVPN 1"
	outbound, _ := config.FindOutbound("OpenVPN 1")
	outbound.ID = "Office VPN"
	require.NoError(t, config.UpdateOutbound("OpenVPN 1", outbound))
	require.NoError(t, config.Validate())
	require.Equal(t, []string{"HOST 1", "Office VPN"}, config.Fallbacks[0].Outbounds)
	require.Equal(t, "Office VPN", config.Routes[1].Target)
	require.Equal(t, "Fallback 1", config.Routes[2].Target)
	require.Equal(t, Route{Key: config.Routes[0].Key, ID: "Routes of Office VPN",
		Directive: "IP-CIDR", Target: "Office VPN", Params: []string{"10.0.0.0/8"},
		Provided: "Office VPN", Enabled: true}, config.Routes[0])
	require.Equal(t, "Office VPN", config.Settings.DefaultTarget)
	require.False(t, config.TargetExists("OpenVPN 1"))
	{
		config.Settings.DefaultTarget = "Fallback 1"
		require.NoError(t, config.UpdateFallback("Fallback 1", Fallback{ID: "Main chain",
			Enabled: true, HealthCheck: true, Outbounds: []string{"HOST 1"}}))
		require.NoError(t, config.Validate())
		require.Equal(t, "Main chain", config.Fallbacks[0].ID)
		require.True(t, config.Fallbacks[0].HealthCheck)
		require.Equal(t, "Main chain", config.Routes[2].Target)
		require.Equal(t, "Main chain", config.Settings.DefaultTarget)
	}
	{
		key := config.Routes[0].Key
		config.Routes[0].ID = "custom"
		outbound.ID = "VPN 2"
		require.NoError(t, config.UpdateOutbound("Office VPN", outbound))
		require.Equal(t, "custom", config.Routes[0].ID)
		require.Equal(t, "VPN 2", config.Routes[0].Provided)
		require.Equal(t, key, config.Routes[0].Key)
	}
}

func TestUpdateUser_LastAdministrator(t *testing.T) {
	config := makeConfig(t)
	require.NoError(t, config.AddUser(User{Name: "admin", Role: RoleAdministrator, PasswordHash: "h"}))
	require.NoError(t, config.AddUser(User{Name: "viewer", Role: RoleUser, PasswordHash: "h"}))
	require.ErrorContains(t, config.AddUser(User{Name: "admin", Role: RoleUser, PasswordHash: "h"}),
		"user 'admin' already exists")
	require.ErrorContains(t, config.AddUser(User{Name: "Bad Name", Role: RoleUser,
		PasswordHash: "h"}), "invalid user name 'Bad Name'")
	require.ErrorContains(t, config.AddUser(User{Name: "x", Role: "root", PasswordHash: "h"}),
		"unexpected role 'root'")
	require.ErrorContains(t, config.AddUser(User{Name: "x", Role: RoleUser}),
		"password of 'x' does not exist")
	require.ErrorContains(t, config.UpdateUser("admin", RoleOperator, ""),
		"last administrator cannot be removed")
	require.ErrorContains(t, config.UpdateUser("missing", "", ""), "does not exist")
	require.ErrorContains(t, config.UpdateUser("viewer", "root", ""), "unexpected role")
	config.Users[0].Role = RoleAdministrator
	require.NoError(t, config.UpdateUser("viewer", RoleAdministrator, "new"))
	user, found := config.FindUser("viewer")
	require.True(t, found)
	require.Equal(t, User{Name: "viewer", Role: RoleAdministrator, PasswordHash: "new", Epoch: 1},
		user)
	require.NoError(t, config.RemoveUser("admin"))
	require.ErrorContains(t, config.RemoveUser("viewer"), "last administrator cannot be removed")
	require.ErrorContains(t, config.RemoveUser("admin"), "does not exist")
	_, found = config.FindUser("admin")
	require.False(t, found)
	require.True(t, IsRole(RoleOperator))
	require.False(t, IsRole(RoleAnonymous))
}

func TestValidate_Problems(t *testing.T) {
	for _, item := range []struct {
		mutate  func(config *Config)
		message string
	}{
		{func(config *Config) { config.Users = []User{{Name: "a", Role: RoleUser}} }, "password of 'a'"},
		{func(config *Config) {
			config.Users = []User{{Name: "a", Role: RoleUser, PasswordHash: "h"},
				{Name: "a", Role: RoleUser, PasswordHash: "h"}}
		}, "duplicate user 'a'"},
		{func(config *Config) { config.Outbounds[1].ID = "HOST 1" }, "duplicate outbound ID 'HOST 1'"},
		{func(config *Config) { config.Outbounds[1].Slot = 1 }, "duplicate outbound slot (1)"},
		{func(config *Config) { config.Outbounds[1].Slot = 0 }, "invalid slot (0)"},
		{func(config *Config) { config.Outbounds[1].Type = "x" }, "unexpected outbound type 'x'"},
		{func(config *Config) { config.Fallbacks = []Fallback{{ID: "HOST 1"}} },
			"duplicate fallback ID 'HOST 1'"},
		{func(config *Config) { config.Fallbacks = []Fallback{{ID: "f", Outbounds: []string{"x"}}} },
			"failed to check fallback 'f'"},
		{func(config *Config) { config.Routes = []Route{{Directive: "DOMAIN"}} },
			"duplicate route key ''"},
		{func(config *Config) { config.Routes = []Route{{Key: "k", Directive: "DOMAIN"}} },
			"failed to check route 1"},
		{func(config *Config) { config.Settings.CheckInterval = 1 }, "invalid check interval (1)"},
		{func(config *Config) { config.Settings.ProbeTarget = "host" }, "failed to check probe target"},
		{func(config *Config) { config.Settings.DefaultTarget = "x" }, "default target 'x'"},
		{func(config *Config) { config.Settings.BindAddress = "all" }, "invalid bind address 'all'"},
		{func(config *Config) { config.Settings.UnreachableLimit = 10001 },
			"invalid number of unreachable routes (10001), expected 1..10000"},
		{func(config *Config) { config.Settings.UnreachableLifetime = 59 },
			"invalid lifetime of unreachable routes (59), expected 60..31536000"},
		{func(config *Config) { config.Settings.LogLines = 9 },
			"invalid number of log lines (9), expected 10..100000"},
		{func(config *Config) { config.Settings.VerboseLogLines = 100001 },
			"invalid number of verbose log lines (100001), expected 10..100000"},
		{func(config *Config) { config.Settings.TimeServers = make([]string, 11) },
			"invalid number of time servers (11), expected 1..10"},
		{func(config *Config) { config.Settings.TimeTolerance = 601 },
			"invalid time tolerance (601), expected 1..600"},
		{func(config *Config) { config.Settings.TimeInterval = 59 },
			"invalid time check interval (59), expected 60..86400"},
		{func(config *Config) { config.Settings.TimeWait = 4 },
			"invalid time synchronization wait (4), expected 5..600"},
		{func(config *Config) { config.Settings.TimeServers = []string{"bad server"} },
			"invalid time server 'bad server'"},
		{func(config *Config) { config.Settings.TimeServers = []string{"ntp.example:0"} },
			"invalid port of time server 'ntp.example:0'"},
		{func(config *Config) { config.Inbounds[1].ID = "SOCKS5 1" },
			"duplicate inbound ID 'SOCKS5 1'"},
		{func(config *Config) { config.Inbounds[1].Port = InboundPortFirst },
			"duplicate inbound port (7800)"},
		{func(config *Config) { config.Inbounds[0].Type = "x" },
			"failed to check inbound 'SOCKS5 1': unexpected inbound type 'x'"},
		{func(config *Config) { config.Inbounds[0].Outbound = "bad/id" },
			"failed to check inbound 'SOCKS5 1': invalid outbound 'bad/id'"},
		{func(config *Config) { config.Inbounds[2].OpenVPN.Network = "10.77.2.0/25" },
			"network of inbound 'OpenVPN 2' overlaps inbound 'OpenVPN 1'"},
		{func(config *Config) { config.Inbounds[1].OpenVPN.OTP = true },
			"failed to check inbound 'OpenVPN 1': OTP key does not exist"},
	} {
		config := makeConfig(t)
		for _, kind := range []string{TypeSocks5, TypeOpenVPN, TypeOpenVPN} {
			_, err := config.AddInbound(Inbound{Type: kind}, time.Now())
			require.NoError(t, err)
		}
		item.mutate(config)
		require.ErrorContains(t, config.Validate(), item.message)
	}
	{
		config := makeConfig(t)
		config.Settings.UnreachableLimit, config.Settings.UnreachableLifetime = 0, 0
		config.Settings.LogLines, config.Settings.VerboseLogLines = 0, 0
		config.Settings.TimeServers, config.Settings.TimeTolerance = nil, 0
		config.Settings.TimeInterval, config.Settings.TimeWait = 0, 0
		config.Inbounds = nil
		require.NoError(t, config.Validate())
		require.Equal(t, []string{"time.cloudflare.com", "time.google.com", "pool.ntp.org"},
			config.Settings.TimeServers)
		require.Equal(t, []int{1, 3600, 60}, []int{config.Settings.TimeTolerance,
			config.Settings.TimeInterval, config.Settings.TimeWait})
		config.Settings.TimeServers = []string{"10.0.0.1", "ntp.example:1123", "[fd00::1]:123"}
		config.Settings.TimeTolerance, config.Settings.TimeInterval = 5, 600
		require.NoError(t, config.Validate())
		servers, tolerance, interval := config.Settings.TimeSync()
		require.Equal(t, defaultTimeServers, servers)
		require.Equal(t, []int{1, 3600}, []int{tolerance, interval})
		config.Settings.TimeManual = true
		servers, tolerance, interval = config.Settings.TimeSync()
		require.Equal(t, config.Settings.TimeServers, servers)
		require.Equal(t, []int{5, 600}, []int{tolerance, interval})
		clone := config.Clone()
		clone.Settings.TimeServers[0] = "changed"
		require.Equal(t, "10.0.0.1", config.Settings.TimeServers[0])
		require.Equal(t, 100, config.Settings.UnreachableLimit)
		require.Equal(t, 86400, config.Settings.UnreachableLifetime)
		require.Equal(t, 300, config.Settings.LogLines)
		require.Equal(t, 3000, config.Settings.VerboseLogLines)
	}
}
