package server

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/tools"
)

// NOTE: A fake probe listener: it answers CONNECT, then the test plays the log line of the
// router through observe, keyed by the port of the client.
func startFakeProxy(t *testing.T, react func(target string, port int,
	connection net.Conn)) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				request, err := http.ReadRequest(bufio.NewReader(connection))
				if err != nil {
					return
				}
				_, _ = connection.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				react(request.RequestURI, connection.RemoteAddr().(*net.TCPAddr).Port, connection)
			}()
		}
	}()
	return listener.Addr().String()
}

func TestParseTrial_Lines(t *testing.T) {
	result := parseTrial("[TCP] 127.0.0.1:4000 --> a.test:443 match DomainSuffix(a.test) using "+
		"Fallback 1[SOCKS5 1]", time.Second)
	require.Equal(t, trialResult{rule: "DomainSuffix(a.test)", chain: []string{"Fallback 1",
		"SOCKS5 1"}, elapsed: time.Second}, result)
	result = parseTrial("[TCP] 127.0.0.1:4000(curl) --> a.test:443 doesn't match any rule using "+
		"DIRECT", time.Second)
	require.Equal(t, "Match", result.rule)
	result = parseTrial("[TCP] dial HOST 1 (match IPCIDR/10.0.0.0/8) 127.0.0.1:4000 --> "+
		"10.1.1.1:22 error: connect: connection refused", time.Second)
	require.Equal(t, "IPCIDR(10.0.0.0/8)", result.rule)
	require.Equal(t, []string{"HOST 1"}, result.chain)
	require.EqualError(t, result.err, "connect: connection refused")
	require.Equal(t, "Match", readableRule("Match/"))
	require.Equal(t, "Odd", readableRule("Odd"))
	require.Equal(t, "", parseTrial("unrelated", 0).rule)
}

func TestServer_Tools(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/users",
		userRequest{Name: "viewer", Password: "viewer123", Confirm: "viewer123",
			Role: config.RoleUser})
	require.Equal(t, http.StatusOK, code, body)
	viewer := login(t, server, "viewer", "viewer123")
	for _, path := range []string{"/api/tools/lookup", "/api/tools/ping"} {
		code, _, _ = call(t, server, viewer, http.MethodPost, path, toolRequest{Host: "a.test"})
		require.Equal(t, http.StatusForbidden, code)
		code, body, _ = call(t, server, admin, http.MethodPost, path, toolRequest{Host: ""})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "host does not exist", body["error"])
		code, body, _ = call(t, server, admin, http.MethodPost, path, toolRequest{Host: "a.test"})
		require.Equal(t, http.StatusConflict, code)
		require.Equal(t, "router is not running", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, path, "bad")
		require.Equal(t, http.StatusBadRequest, code)
	}
	require.NotZero(t, server.probe.port)
	require.Len(t, server.probe.secret, 32)
	require.Equal(t, "127.0.0.1:7899", server.mixedAddress(config.Settings{MixedPort: 7899,
		BindAddress: "*"}))
	require.Equal(t, "10.0.0.2:7899", server.mixedAddress(config.Settings{MixedPort: 7899,
		BindAddress: "10.0.0.2"}))
	_, err := server.probeUser("missing")
	require.EqualError(t, err, "outbound 'missing' is not active in the router")
}

func TestServer_Trial(t *testing.T) {
	server := makeServer(t)
	good := "[TCP] 127.0.0.1:%v --> %v match DomainSuffix(good.test) using Fallback 1[SOCKS5 1]"
	bad := "[TCP] dial HOST 1 (match Match/) 127.0.0.1:%v --> %v error: connect: refused"
	proxy := startFakeProxy(t, func(target string, port int, connection net.Conn) {
		switch target {
		case "good.test:80", "kept.test:80":
			server.observe(fmt.Sprintf(good, port, target))
			_, _ = io.Copy(io.Discard, connection)
		case "closing.test:80":
			server.observe(fmt.Sprintf(good, port, target))
			time.Sleep(100 * time.Millisecond)
		case "bad.test:80":
			server.observe(fmt.Sprintf(bad, port, target))
		case "slow.test:80":
			_, _ = io.Copy(io.Discard, connection)
		}
	})
	none := func([]string) time.Duration { return 0 }
	settle := func([]string) time.Duration { return 400 * time.Millisecond }
	ctx := context.Background()
	result := server.trial(ctx, proxy, "probe1", "good.test:80", none)
	require.NoError(t, result.err)
	require.Equal(t, "DomainSuffix(good.test)", result.rule)
	require.Equal(t, []string{"Fallback 1", "SOCKS5 1"}, result.chain)
	require.Positive(t, result.elapsed)
	result = server.trial(ctx, proxy, "", "bad.test:80", none)
	require.EqualError(t, result.err, "connect: refused")
	require.Equal(t, "Match", result.rule)
	result = server.trial(ctx, proxy, "", "silent.test:80", none)
	require.EqualError(t, result.err, "connection closed by the router")
	result = server.trial(ctx, proxy, "", "closing.test:80", settle)
	require.EqualError(t, result.err, "closed by the far side: the proxy could not reach the target")
	require.True(t, result.proxied)
	result = server.trial(ctx, proxy, "", "kept.test:80", settle)
	require.NoError(t, result.err)
	require.True(t, result.proxied)
	{
		ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		result = server.trial(ctx, proxy, "", "slow.test:80", none)
		require.EqualError(t, result.err, "router did not report the connection")
		result = server.trial(ctx, "127.0.0.1:1", "", "slow.test:80", none)
		require.ErrorContains(t, result.err, "failed to connect to router '127.0.0.1:1'")
	}
	require.Empty(t, server.probe.waiters)
	snapshot := config.Default()
	snapshot.Outbounds = []config.Outbound{{ID: "SOCKS5 1", Type: config.TypeSocks5},
		{ID: "HOST 1", Type: config.TypeHost}}
	settleFor := server.settleFor(snapshot, time.Second)
	require.Equal(t, time.Second, settleFor([]string{"Fallback 1", "SOCKS5 1"}))
	require.Zero(t, settleFor([]string{"HOST 1"}))
	require.Zero(t, settleFor(nil))
	require.Equal(t, time.Second, settleProxy(snapshot.Outbounds[0], time.Second)(nil))
	require.Zero(t, settleProxy(snapshot.Outbounds[1], time.Second)(nil))
}

func TestServer_Inspect(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/outbounds", config.Outbound{
		Type: config.TypeSocks5, Enabled: true, Socks5: &config.Socks5Options{Server: "127.0.0.1",
			Port: 1080}, DNS: config.DNSOptions{Servers: []string{"tcp://192.0.2.53"}}})
	require.Equal(t, http.StatusOK, code, body)
	code, body, _ = call(t, server, admin, http.MethodPost, "/api/routes", config.Route{
		Directive: "DOMAIN-SUFFIX", Target: "SOCKS5 1", Enabled: true, Params: []string{"good.test"}})
	require.Equal(t, http.StatusOK, code, body)
	good := "[TCP] 127.0.0.1:%v --> %v match DomainSuffix(good.test) using SOCKS5 1"
	proxy := startFakeProxy(t, func(target string, port int, connection net.Conn) {
		switch target {
		case "good.test:80":
			server.observe(fmt.Sprintf(good, port, target))
			_, _ = io.Copy(io.Discard, connection)
		case "192.0.2.53:53":
			for {
				size := make([]byte, 2)
				if _, err := io.ReadFull(connection, size); err != nil {
					return
				}
				query := make([]byte, binary.BigEndian.Uint16(size))
				if _, err := io.ReadFull(connection, query); err != nil {
					return
				}
				answer := append([]byte{}, query...)
				answer[2], answer[3] = 0x81, 0x80
				if query[len(query)-3] == tools.TypeA {
					answer[7] = 1
					answer = append(answer, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 192, 0, 2, 7)
				}
				_, _ = connection.Write(append(binary.BigEndian.AppendUint16(nil,
					uint16(len(answer))), answer...))
			}
		}
	})
	_, text, err := net.SplitHostPort(proxy)
	require.NoError(t, err)
	port, err := strconv.Atoi(text)
	require.NoError(t, err)
	require.NoError(t, server.store.Update(func(config *config.Config) error {
		config.Settings.MixedPort, config.Settings.BindAddress = port, "127.0.0.1"
		return nil
	}))
	server.mutex.Lock()
	server.running, server.probe.port = false, port
	server.mutex.Unlock()
	server.synchronize()
	require.Equal(t, proxy, server.probeAddress())
	ctx := context.Background()
	result := server.inspect(ctx, toolRequest{Host: "good.test", Port: 80})
	require.Equal(t, "DomainSuffix(good.test)", result.Route.Rule)
	require.Equal(t, "SOCKS5 1", result.Route.Target)
	require.True(t, result.Route.Proxied)
	require.Empty(t, result.Route.Error)
	require.NotEmpty(t, result.Problem)
	require.Len(t, result.Outbounds, 2)
	direct, socks := result.Outbounds[0], result.Outbounds[1]
	require.Equal(t, config.TargetDirect, direct.ID)
	require.Equal(t, link.StateUp, direct.State)
	require.Equal(t, "system", direct.Resolvers[0].Server)
	require.NotEmpty(t, direct.Resolvers[0].Error)
	require.Empty(t, direct.Connect.Error)
	require.False(t, direct.Connect.Proxied)
	require.Equal(t, []resolverCheck{{Server: "tcp://192.0.2.53#SOCKS5 1",
		Addresses: []string{"192.0.2.7"}, Latency: socks.Resolvers[0].Latency}}, socks.Resolvers)
	require.True(t, socks.Connect.Proxied)
	require.Empty(t, socks.Connect.Error)
	{
		result := server.inspect(ctx, toolRequest{Host: "127.0.0.1", Port: 80})
		require.Equal(t, []string{"127.0.0.1"}, result.Addresses)
		require.Empty(t, result.Outbounds[0].Resolvers)
		require.Equal(t, "connection closed by the router", result.Route.Error)
		require.Equal(t, "connection closed by the router", result.Outbounds[1].Connect.Error)
	}
	{
		require.Equal(t, "DNS over 'quic' is not checked",
			server.resolveVia(ctx, "quic://192.0.2.53#SOCKS5 1", "a.test").Error)
		require.Equal(t, "outbound 'Gone' is not active in the router",
			server.resolveVia(ctx, "tcp://192.0.2.53#Gone", "a.test").Error)
		require.Contains(t, server.resolveVia(ctx, "bad://x", "a.test").Error, "unexpected scheme")
	}
	tool := filepath.Join(t.TempDir(), "ping")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/bash\nprintf '%s\\n' \"$*\" > \"$0.args\"\n"+
		"echo '64 bytes from 127.0.0.1: icmp_seq=1 ttl=64 time=0.05 ms'\n"+
		"echo '64 bytes from 127.0.0.1: icmp_seq=2 ttl=64 time=0.07 ms'\n"), 0o700))
	server.options.Tools.Ping = tool
	pinged := server.pingAll(ctx, toolRequest{Host: "127.0.0.1", Port: 80, Count: 2},
		netip.MustParseAddr("127.0.0.1"))
	require.Equal(t, "127.0.0.1", pinged.Address)
	require.Equal(t, config.TargetDirect, pinged.System)
	require.Equal(t, methodICMP, pinged.Outbounds[0].Method)
	require.Len(t, pinged.Outbounds[0].Replies, 2)
	arguments, err := os.ReadFile(tool + ".args")
	require.NoError(t, err)
	require.Equal(t, "-n -c 2 -i 0.2 -W 1 127.0.0.1\n", string(arguments))
	require.Equal(t, methodTCP, pinged.Outbounds[1].Method)
	require.Empty(t, pinged.Outbounds[1].Replies)
	require.Equal(t, "connection closed by the router", pinged.Outbounds[1].Error)
	{
		require.NoError(t, os.WriteFile(tool, []byte("#!/bin/bash\necho 'ping: unreachable'\n"+
			"exit 1\n"), 0o700))
		pinged := server.pingAll(ctx, toolRequest{Host: "127.0.0.1", Port: 80, Count: 1},
			netip.MustParseAddr("127.0.0.1"))
		require.Equal(t, "ping: unreachable", pinged.Outbounds[0].Error)
	}
}

func TestSystemPing_Rules(t *testing.T) {
	snapshot := config.Default()
	snapshot.Outbounds = []config.Outbound{{ID: "Tunnel", Type: config.TypeAmneziaWG, Slot: 4,
		Enabled: true, AmneziaWG: &config.AmneziaWGOptions{Config: sampleTunnel}},
		{ID: "Proxy", Type: config.TypeSocks5, Slot: 5, Enabled: true,
			Socks5: &config.Socks5Options{Server: "10.0.0.1", Port: 1080}}}
	snapshot.Routes = []config.Route{
		{Key: "a", Directive: "IP-CIDR", Target: "Tunnel", Enabled: true, Params: []string{"10.8.0.0/16"}},
		{Key: "b", Directive: "IP-CIDR", Target: "Proxy", Enabled: true, Params: []string{"10.9.0.0/16"}}}
	links := map[string]link.Status{"Tunnel": {State: link.StateUp}, "Proxy": {State: link.StateUp}}
	via, blocked := systemPing(snapshot, links, netip.MustParseAddr("10.8.1.1"))
	require.Equal(t, "Tunnel", via)
	require.False(t, blocked)
	via, blocked = systemPing(snapshot, links, netip.MustParseAddr("10.9.1.1"))
	require.Equal(t, "", via)
	require.True(t, blocked)
	via, _ = systemPing(snapshot, links, netip.MustParseAddr("192.0.2.1"))
	require.Equal(t, config.TargetDirect, via)
	require.Equal(t, "last line", lastOutputLine("first\n\nlast line\n"))
	require.Equal(t, "", lastOutputLine("\n"))
	require.Equal(t, []string{"A", "B"}, parseChain("A[B]"))
}

func TestToolRequest_Normalize(t *testing.T) {
	for text, expected := range map[string]toolRequest{
		"a.test":                       {Host: "a.test", Port: 443, Count: 5},
		"a.test:22":                    {Host: "a.test", Port: 22, Count: 5},
		"http://a.test/path":           {Host: "a.test", Port: 80, Count: 5},
		"https://[2001:db8::1]:8443/x": {Host: "2001:db8::1", Port: 8443, Count: 5},
		" 10.0.0.1 ":                   {Host: "10.0.0.1", Port: 443, Count: 5}} {
		request := toolRequest{Host: text}
		require.NoError(t, request.normalize(), text)
		require.Equal(t, expected, request, text)
	}
	request := toolRequest{Host: "a.test", Port: 25, Count: 99}
	require.NoError(t, request.normalize())
	require.Equal(t, toolRequest{Host: "a.test", Port: 25, Count: maxPingCount}, request)
	for _, text := range []string{"", "a b", "user@a.test", "http://%zz"} {
		request := toolRequest{Host: text}
		require.Error(t, request.normalize(), text)
	}
	request = toolRequest{Host: "a.test", Port: 70000}
	require.EqualError(t, request.normalize(), "invalid port (70000)")
}
