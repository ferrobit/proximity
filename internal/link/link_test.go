package link

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/clock"
	"proximity/internal/config"
	"proximity/internal/wgconf"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"DNS = 10.8.1.1\n[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = 127.0.0.1:51820\n" +
	"AllowedIPs = 0.0.0.0/0, 10.55.0.9, 10.66.0.0/16\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\ndaemon\n"
const sampleAddress = "203.0.113.9"
const fakeIP = `#!/bin/bash
echo "$*" >> "$(dirname "$0")/ip.log"
case "$*" in
"-4 route show table "*" dev "*)
	printf 'default dev x\n10.99.0.0/16 via 10.88.0.1 dev x\n10.77.1.5 dev x\n'
	;;
"-6 route show table "*" dev "*)
	printf 'fd00::/64 dev x\nunreachable default\n'
	;;
"-4 rule show")
	printf '5100:\tfrom all fwmark 0xc73d lookup 51005\n'
	;;
*"rule del"*)
	exit 1
	;;
esac
exit 0
`
const fakeOpenVPN = `#!/bin/bash
echo "$*" > "$(dirname "$0")/openvpn.log"
env | grep -E '^(FAKETIME|LD_PRELOAD)=' | sort > "$(dirname "$0")/environment.log"
hook=""
while [ $# -gt 0 ]; do
	if [ "$1" = "--up" ]; then hook="${2##* }"; fi
	shift
done
if [ -f "$(dirname "$0")/stubborn" ]; then trap '' TERM; fi
if [ -f "$(dirname "$0")/down" ]; then
	echo "2026-01-01 00:00:00 TCP: connect to [AF_INET]192.0.2.1:1194 failed: Connection refused"
	exit 1
fi
exec 3<>/dev/tty
printf 'Enter Auth Username:' >&3
read -r user <&3
printf 'Enter Auth Password:' >&3
read -r pass <&3
if [ "$user" = "denied" ]; then
	echo "2026-01-01 00:00:00 AUTH: Received control message: AUTH_FAILED"
	exec sleep 30
fi
if [ "$user" = "locked" ]; then
	echo "2026-01-01 00:00:00 OpenSSL: error:1C800064:Provider routines::bad decrypt:"
	exec sleep 30
fi
if [ "$user" = "untrusted" ]; then
	echo "2026-01-01 00:00:00 VERIFY ERROR: depth=1, error=self-signed certificate: CN=Evil CA"
	exec sleep 30
fi
printf 'CHALLENGE: Enter OTP ' >&3
read -r otp <&3
echo "$user|$pass|$otp" > "$(dirname "$hook")/../answers.txt"
cat > "$hook" <<JSON
{"route_vpn_gateway":"10.88.0.1","ifconfig_local":"10.88.0.2","route_network_1":"10.99.0.0",
"route_netmask_1":"255.255.0.0","route_gateway_1":"10.88.0.1","route_network_2":"10.1.0.0",
"route_netmask_2":"bad","route_ipv6_network_1":"fd00::/64",
"foreign_option_1":"dhcp-option DNS 10.99.0.1",
"foreign_option_2":"dhcp-option DOMAIN Corp.Test.",
"foreign_option_3":"dhcp-option WINS 10.99.0.2","foreign_option_4":"something else"}
JSON
echo "2026-01-01 00:00:00 Initialization Sequence Completed"
exec sleep 30
`

const fakeServer = `#!/bin/bash
echo "$*" > "$(dirname "$0")/server.log"
if [ -f "$(dirname "$0")/fail" ]; then
	echo "2026-01-01 00:00:00 Options error: boom"
	echo "Use --help for more information."
	exit 1
fi
echo "2026-01-01 00:00:00 MANAGEMENT: unix domain socket listening"
echo "2026-01-01 00:00:00 Initialization Sequence Completed"
echo "2026-01-01 00:00:01 TLS Error: certificate hash verification failed."
exec sleep 30
`
const fakeFirewall = `#!/bin/bash
echo "$*" >> "$(dirname "$0")/iptables.log"
port="$(cat "$(dirname "$0")/port.txt" 2>/dev/null)"
case "$*" in
*"-C FORWARD"* | *"-D "*)
	exit 1
	;;
*"-S FORWARD"*)
	printf -- '-P FORWARD DROP\n-A FORWARD -i proximity_i9 -j ACCEPT\n'
	printf -- '-A FORWARD -o docker0 -j ACCEPT\n'
	;;
*"-v -S PROXIMITY-STAT-IN"*)
	printf -- '-N PROXIMITY-STAT-IN\n'
	printf -- '-A PROXIMITY-STAT-IN -p tcp -m tcp --dport %s -c 5 300\n' "$port"
	printf -- '-A PROXIMITY-STAT-IN -p udp -m udp --dport %s -c 1 50\n' "$port"
	;;
*"-v -S PROXIMITY-STAT-OUT"*)
	printf -- '-A PROXIMITY-STAT-OUT -p tcp -m tcp --sport %s -c 9 7000\n' "$port"
	;;
esac
exit 0
`
const sampleStatus = "TITLE,OpenVPN\nHEADER,CLIENT_LIST,Common Name,Real Address\n" +
	"CLIENT_LIST,bob,192.0.2.5:5000,10.77.2.2,,100,200,2026-01-01 00:00:00,1700000000,bob,0,0,A\n" +
	"CLIENT_LIST,UNDEF,192.0.2.6:5001,10.77.2.3,,1,2,2026-01-01 00:00:01,1700000001,UNDEF,1,1,A\n" +
	"CLIENT_LIST,UNDEF,192.0.2.7:5002,,,0,0,2026-01-01 00:00:02,1700000002,UNDEF,2,2,A\n" +
	"CLIENT_LIST,short\nROUTING_TABLE,10.77.2.2,bob\nEND\n"

func sampleResolve(_ context.Context, host string) ([]string, error) {
	if host != "vpn.example.com" {
		return nil, fmt.Errorf("unexpected name '%v'", host)
	}
	return []string{sampleAddress}, nil
}

func writeScript(t *testing.T, directory string, name string, text string) string {
	path := filepath.Join(directory, name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o700))
	return path
}

func makeTools(t *testing.T) (Tools, string) {
	directory := t.TempDir()
	return Tools{IP: writeScript(t, directory, "ip", fakeIP),
		OpenVPN:   writeScript(t, directory, "openvpn", fakeOpenVPN),
		Ping:      writeScript(t, directory, "ping", "#!/bin/bash\nexit 0\n"),
		AmneziaGo: writeScript(t, directory, "amneziawg-go", "#!/bin/bash\necho boom\nexit 1\n"),
		AmneziaWG: writeScript(t, directory, "awg", "#!/bin/bash\nexit 0\n"),
		Oathtool:  filepath.Join(directory, "missing")}, directory
}

func makeListener(t *testing.T) string {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return listener.Addr().String()
}

func makeInboundPort(t *testing.T) (int, net.Listener) {
	for port := config.InboundPortLast; port > config.InboundPortFirst; port-- {
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			t.Cleanup(func() {
				_ = listener.Close()
			})
			return port, listener
		}
	}
	require.FailNow(t, "free inbound port does not exist")
	return 0, nil
}

func makeManagement(t *testing.T, path string) func() []string {
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
	})
	mutex, commands := sync.Mutex{}, []string{}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = connection.Write([]byte(">INFO:OpenVPN Management Interface Version 5\n"))
			line, _ := bufio.NewReader(connection).ReadString('\n')
			mutex.Lock()
			commands = append(commands, strings.TrimSpace(line))
			mutex.Unlock()
			if strings.HasPrefix(line, "status") {
				_, _ = connection.Write([]byte(sampleStatus))
			} else if strings.HasPrefix(line, "client-kill 0 HALT") {
				_, _ = connection.Write([]byte("SUCCESS: client-kill command succeeded\n"))
			} else if strings.HasPrefix(line, "kill UNDEF") {
				_, _ = connection.Write([]byte("ERROR: common name 'UNDEF' not found\n"))
			} else {
				_, _ = connection.Write([]byte("partial\n"))
			}
			_ = connection.Close()
		}
	}()
	return func() []string {
		mutex.Lock()
		defer mutex.Unlock()
		return append([]string{}, commands...)
	}
}

func waitInbound(t *testing.T, manager *Manager, id string, state string) InboundStatus {
	require.Eventually(t, func() bool {
		return manager.Inbounds()[id].State == state
	}, 20*time.Second, 20*time.Millisecond, "inbound '%v' state '%v'", id, state)
	return manager.Inbounds()[id]
}

func waitState(t *testing.T, manager *Manager, id string, state string) Status {
	require.Eventually(t, func() bool {
		return manager.Status()[id].State == state
	}, 15*time.Second, 20*time.Millisecond, "outbound '%v' state '%v'", id, state)
	return manager.Status()[id]
}

func TestAmneziaDriver_Ready(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "lo.sock")
	{
		driver := &amneziaDriver{device: "proximity_missing"}
		require.False(t, driver.ready(socket))
	}
	{
		driver := &amneziaDriver{device: "lo"}
		require.False(t, driver.ready(socket))
		require.NoError(t, os.WriteFile(socket, nil, 0o600))
		require.False(t, driver.ready(socket))
		require.NoError(t, os.Remove(socket))
		listener, err := net.Listen("unix", socket)
		require.NoError(t, err)
		defer listener.Close()
		require.True(t, driver.ready(socket))
	}
}

func TestCertificateFingerprint_Values(t *testing.T) {
	text := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("proximity")}))
	require.Equal(t, "69:4D:46:9D:39:D4:00:00:E9:74:C4:31:C6:EB:FF:06:0E:12:F6:F5:49:59:7B:B9:"+
		"F5:3E:17:6B:32:E5:92:61", certificateFingerprint(text))
	require.Empty(t, certificateFingerprint("not a certificate"))
	require.Empty(t, certificateFingerprint(""))
}

func TestClaimCode_Owners(t *testing.T) {
	directory := t.TempDir()
	path, now := filepath.Join(directory, usedFile), time.Unix(1700000000, 0)
	step := now.Unix() / 30
	require.NoError(t, claimCode(path, step, "bob", "203.0.113.5", now))
	require.NoError(t, claimCode(path, step, "bob", "203.0.113.5", now))
	require.EqualError(t, claimCode(path, step, "bob", "203.0.113.9", now),
		"one-time code of user 'bob' is already used")
	require.EqualError(t, claimCode(path, step, "eve", "203.0.113.5", now),
		"one-time code of user 'eve' is already used")
	require.NoError(t, claimCode(path, step+1, "eve", "203.0.113.5", now))
	{
		require.NoError(t, claimCode(path, step-1, "bob", "", now))
		require.EqualError(t, claimCode(path, step-1, "bob", "", now),
			"one-time code of user 'bob' is already used")
	}
	{
		later := now.Add(5 * otpStep)
		require.NoError(t, claimCode(path, step+5, "bob", "203.0.113.5", later))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.JSONEq(t, `{"`+strconv.FormatInt(step+5, 10)+
			`":{"name":"bob","address":"203.0.113.5"}}`, string(data))
	}
	{
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
		require.NoError(t, claimCode(path, step, "eve", "203.0.113.9", now))
		require.EqualError(t, claimCode(path, step, "bob", "203.0.113.9", now),
			"one-time code of user 'bob' is already used")
	}
	{
		blocked := filepath.Join(directory, "blocked")
		require.NoError(t, os.WriteFile(blocked, []byte("x"), 0o600))
		require.ErrorContains(t, claimCode(filepath.Join(blocked, usedFile), step, "bob", "", now),
			"failed to")
	}
}

func TestClientConfig_Export(t *testing.T) {
	snapshot := config.Default()
	inbound, err := snapshot.AddInbound(config.Inbound{Type: config.TypeOpenVPN, ID: "Office",
		Users:   []config.InboundUser{{Enabled: true, Name: "bob", Password: "secret"}},
		OpenVPN: &config.OpenVPNServer{Server: "vpn.example.com"}}, time.Now())
	require.NoError(t, err)
	server := inbound.OpenVPN
	text, err := ClientConfig(inbound, ExportOptions{User: "bob", Host: "ignored.test"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(text, "# Proximity inbound 'Office'\nclient\ndev tun\n"+
		"proto udp\nremote vpn.example.com 7800\nresolv-retry infinite\nnobind\npersist-key\n"+
		"persist-tun\nremote-cert-tls server\nverify-x509-name proximity-server name\n"+
		"auth SHA256\ndata-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305\n"+
		"tls-version-min 1.2\ntun-mtu 1420\nexplicit-exit-notify 1\nauth-user-pass\nverb 3\n"+
		"<ca>\n-----BEGIN CERTIFICATE-----\n"), text)
	require.Contains(t, text, "<ca>\n"+server.CACertificate+"</ca>\n")
	require.Contains(t, text, "<cert>\n"+server.ClientCertificate+"</cert>\n")
	require.Contains(t, text, "<key>\n"+server.ClientKey+"</key>\n")
	require.True(t, strings.HasSuffix(text, "<tls-crypt>\n"+server.TLSCrypt+"</tls-crypt>\n"))
	require.NotContains(t, text, server.Key)
	require.NotContains(t, text, server.CAKey)
	require.NotContains(t, text, "secret")
	require.NotContains(t, text, "static-challenge")
	require.NotContains(t, text, "ignore-unknown-option")
	{
		server.Server, server.PublicPort, server.Protocol = "", 1194, config.ProtocolTCP
		server.OTP, server.RedirectGateway = true, true
		text, err := ClientConfig(inbound, ExportOptions{User: "bob", Embed: true,
			Host: "203.0.113.7"})
		require.NoError(t, err)
		require.Contains(t, text, "proto tcp-client\nremote 203.0.113.7 1194\n")
		require.Contains(t, text, "auth-nocache\nstatic-challenge \"Enter one-time code\" 1\n"+
			"ignore-unknown-option block-outside-dns\nverb 3\n")
		require.NotContains(t, text, "explicit-exit-notify")
		require.NotContains(t, text, "\nauth-user-pass\n")
		require.True(t, strings.HasSuffix(text, "<auth-user-pass>\nbob\nsecret\n"+
			"</auth-user-pass>\n"))
	}
	{
		_, err := ClientConfig(inbound, ExportOptions{Host: "203.0.113.7"})
		require.EqualError(t, err, "anonymous connections are not allowed")
		inbound.Anonymous = true
		text, err := ClientConfig(inbound, ExportOptions{Embed: true, Host: "203.0.113.7"})
		require.NoError(t, err)
		require.NotContains(t, text, "auth-user-pass")
		require.NotContains(t, text, "static-challenge")
		server.Certificate = "broken"
		text, err = ClientConfig(inbound, ExportOptions{Host: "203.0.113.7"})
		require.NoError(t, err)
		require.NotContains(t, text, "verify-x509-name")
	}
	{
		_, err := ClientConfig(inbound, ExportOptions{User: "eve", Host: "203.0.113.7"})
		require.EqualError(t, err, "user 'eve' does not exist")
		_, err = ClientConfig(inbound, ExportOptions{User: "bob"})
		require.EqualError(t, err, "server address does not exist")
		_, err = ClientConfig(config.Inbound{Type: config.TypeSocks5}, ExportOptions{})
		require.EqualError(t, err, "unexpected inbound type 'socks5'")
	}
}

func TestCodeDelay_Values(t *testing.T) {
	now := time.Unix(1700000000, 0)
	step := now.Unix() / 30
	require.Equal(t, 11*time.Second, codeDelay(step, now))
	require.Equal(t, 41*time.Second, codeDelay(step+1, now))
	require.Zero(t, codeDelay(0, now))
	require.Zero(t, codeDelay(step-1, now))
	require.Zero(t, codeDelay(step, now.Add(10*time.Second)))
}

func TestComputeOTP_Vectors(t *testing.T) {
	key := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := computeOTP(key, time.Unix(59, 0))
	require.NoError(t, err)
	require.Equal(t, "287082", code)
	code, err = computeOTP("gezdgnbvgy3tqojq", time.Unix(1111111109, 0))
	require.NoError(t, err)
	require.Len(t, code, 6)
	{
		_, err := computeOTP("not base32!", time.Unix(59, 0))
		require.ErrorContains(t, err, "failed to decode OTP key")
	}
	{
		code, err := GenerateOTP(Tools{Oathtool: "/nonexistent/oathtool"}, key, time.Unix(59, 0))
		require.NoError(t, err)
		require.Equal(t, "287082", code)
	}
	{
		if _, err := exec.LookPath("oathtool"); err == nil {
			code, err := GenerateOTP(Tools{Oathtool: "oathtool"}, key, time.Unix(1111111109, 0))
			require.NoError(t, err)
			require.Equal(t, "081804", code)
		}
	}
}

func TestDefaultTools_Environment(t *testing.T) {
	t.Setenv("PROXIMITY_OPENVPN", "/opt/openvpn")
	t.Setenv("PROXIMITY_IP", "")
	tools := DefaultTools()
	require.Equal(t, "/opt/openvpn", tools.OpenVPN)
	require.Equal(t, "ip", tools.IP)
	require.Equal(t, "amneziawg-go", tools.AmneziaGo)
	require.Equal(t, "awg", tools.AmneziaWG)
	require.Equal(t, "ping", tools.Ping)
	require.Equal(t, "oathtool", tools.Oathtool)
	require.Equal(t, 51007, Table(7))
}

func TestDialProbe_Targets(t *testing.T) {
	target := makeListener(t)
	latency, err := dialProbe(context.Background(), target, 0, "", time.Second)
	require.NoError(t, err)
	require.GreaterOrEqual(t, latency, time.Duration(0))
	{
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		closed := listener.Addr().String()
		require.NoError(t, listener.Close())
		_, err = dialProbe(context.Background(), closed, 0, "", time.Second)
		require.ErrorContains(t, err, "failed to connect to '"+closed+"'")
	}
	{
		_, err := dialProbe(context.Background(), target, 0, "missing0", time.Second)
		require.ErrorContains(t, err, "failed to connect to")
	}
}

func TestFindOverlap_Networks(t *testing.T) {
	networks := []deviceNetwork{{"proximity_i1", netip.MustParsePrefix("10.77.1.0/24")},
		{"proximity_i2", netip.MustParsePrefix("10.77.2.0/24")}}
	require.NoError(t, findOverlap(map[string]string{"ifconfig_local": "10.8.0.2",
		"route_vpn_gateway": "10.8.0.1"}, networks))
	require.NoError(t, findOverlap(map[string]string{"ifconfig_local": "10.77.1.2",
		"route_vpn_gateway": "10.77.1.1"}, []deviceNetwork{}))
	require.NoError(t, findOverlap(map[string]string{"ifconfig_local": "bad"}, networks))
	require.NoError(t, findOverlap(map[string]string{}, networks))
	require.EqualError(t, findOverlap(map[string]string{"ifconfig_local": "10.77.2.9",
		"route_vpn_gateway": "10.77.2.1"}, networks),
		"tunnel address '10.77.2.9' overlaps network '10.77.2.0/24' of inbound device 'proximity_i2'")
	require.EqualError(t, findOverlap(map[string]string{"ifconfig_local": "10.8.0.2",
		"route_vpn_gateway": "10.77.1.1"}, networks),
		"tunnel address '10.77.1.1' overlaps network '10.77.1.0/24' of inbound device 'proximity_i1'")
	for _, network := range inboundNetworks() {
		require.True(t, strings.HasPrefix(network.device, "proximity_i"))
	}
}

func TestInterfaces_Loopback(t *testing.T) {
	interfaces, err := Interfaces()
	require.NoError(t, err)
	for _, item := range interfaces {
		require.NotEqual(t, "lo", item.Name)
		require.NotEmpty(t, item.State)
		require.NotNil(t, item.Addresses)
	}
	require.Equal(t, "up", linkState("lo"))
	require.Equal(t, "absent", linkState("missing0"))
	require.True(t, devicePattern.MatchString("proximity_o12"))
	require.True(t, devicePattern.MatchString("proximity_a1"))
	require.False(t, devicePattern.MatchString("proximity_lan"))
	{
		_, err := readCounter("lo", "rx_bytes")
		require.NoError(t, err)
		_, err = readCounter("missing0", "rx_bytes")
		require.ErrorContains(t, err, "failed to read file")
	}
}

func TestLogBook_Clear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	book := openLogBook(path, maxLogLines)
	for index := range 2*maxLogLines - 1 {
		book.add(fmt.Sprintf("line %v", index))
	}
	book.clear()
	require.Empty(t, book.snapshot())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, data)
	require.Empty(t, openLogBook(path, maxLogLines).snapshot())
	book.add("after")
	book.add("again")
	require.Equal(t, []string{"after", "again"}, book.snapshot())
	require.Equal(t, []string{"after", "again"}, openLogBook(path, maxLogLines).snapshot())
	{
		memory := openLogBook("", maxLogLines)
		memory.add("only in memory")
		memory.clear()
		require.Empty(t, memory.snapshot())
	}
}

func TestLogBook_Rotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	book := openLogBook(path, maxLogLines)
	require.Empty(t, book.snapshot())
	for index := range 2*maxLogLines + 5 {
		book.add(fmt.Sprintf("line %v", index))
	}
	require.Len(t, book.snapshot(), maxLogLines)
	require.Equal(t, fmt.Sprintf("line %v", 2*maxLogLines+4), book.snapshot()[maxLogLines-1])
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.LessOrEqual(t, strings.Count(string(data), "\n"), 2*maxLogLines)
	reopened := openLogBook(path, maxLogLines)
	require.Equal(t, book.snapshot(), reopened.snapshot())
	require.Len(t, openLogBook(path, 10).snapshot(), 10)
	{
		memory := openLogBook("", maxLogLines)
		memory.add("only in memory")
		require.Equal(t, []string{"only in memory"}, memory.snapshot())
		blocked := openLogBook(filepath.Join(path, "sub.log"), maxLogLines)
		blocked.add("not written")
		require.Equal(t, []string{"not written"}, blocked.snapshot())
	}
	{
		verbose := openLogBook("", maxLogLines)
		verbose.resize(maxVerboseLines)
		for index := range maxLogLines + 5 {
			verbose.add(fmt.Sprintf("line %v", index))
		}
		require.Len(t, verbose.snapshot(), maxLogLines+5)
		verbose.resize(maxLogLines)
		require.Len(t, verbose.snapshot(), maxLogLines)
		verbose.add("trimmed")
		require.Len(t, verbose.snapshot(), maxLogLines)
		require.Equal(t, "trimmed", verbose.snapshot()[maxLogLines-1])
	}
}

func TestLookup_Addresses(t *testing.T) {
	answers := map[string][]string{"mixed.test": {"2001:db8::1", "198.18.0.9", "192.0.2.1",
		"::ffff:192.0.2.2", "bad"}, "fake.test": {"198.18.0.7", "198.18.255.1"},
		"six.test": {"2001:db8::2"}}
	manager := &Manager{options: Options{Resolve: func(_ context.Context, host string) ([]string,
		error) {
		if answers[host] == nil {
			return nil, fmt.Errorf("failed to resolve '%v': no such name", host)
		}
		return answers[host], nil
	}}}
	{
		addresses, err := manager.Lookup(context.Background(), "mixed.test")
		require.NoError(t, err)
		require.Equal(t, []string{"192.0.2.1", "192.0.2.2", "2001:db8::1"}, addresses)
		addresses, err = manager.Lookup(context.Background(), "192.0.2.7")
		require.NoError(t, err)
		require.Equal(t, []string{"192.0.2.7"}, addresses)
		addresses, err = manager.Lookup(context.Background(), "[2001:db8::7]")
		require.NoError(t, err)
		require.Equal(t, []string{"2001:db8::7"}, addresses)
	}
	{
		_, err := manager.Lookup(context.Background(), "fake.test")
		require.EqualError(t, err,
			"name 'fake.test' has no usable address (198.18.0.7, 198.18.255.1)")
		_, err = manager.Lookup(context.Background(), "missing.test")
		require.EqualError(t, err, "failed to resolve 'missing.test': no such name")
	}
	{
		target, err := manager.ResolveTarget(context.Background(), "mixed.test:443")
		require.NoError(t, err)
		require.Equal(t, "192.0.2.1:443", target)
		target, err = manager.ResolveTarget(context.Background(), "six.test:123")
		require.NoError(t, err)
		require.Equal(t, "[2001:db8::2]:123", target)
		target, err = manager.ResolveTarget(context.Background(), "mixed.test")
		require.NoError(t, err)
		require.Equal(t, "192.0.2.1", target)
		target, err = manager.ResolveTarget(context.Background(), "192.0.2.9:53")
		require.NoError(t, err)
		require.Equal(t, "192.0.2.9:53", target)
		_, err = manager.ResolveTarget(context.Background(), "missing.test:1")
		require.ErrorContains(t, err, "no such name")
	}
	{
		manager := &Manager{}
		addresses, err := manager.Lookup(context.Background(), "localhost")
		require.NoError(t, err)
		require.Contains(t, addresses, "127.0.0.1")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = manager.Lookup(ctx, "name.invalid")
		require.ErrorContains(t, err, "failed to resolve 'name.invalid'")
	}
}

func TestManager_AmneziaFailure(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory: filepath.Join(directory, "run"), Tools: tools})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	outbound := config.Outbound{ID: "AWG", Type: config.TypeAmneziaWG, Enabled: true, Slot: 4,
		Attempts: 2, ReconnectTimeout: 1, AmneziaWG: &config.AmneziaWGOptions{Config: sampleTunnel},
		Monitoring: &config.Monitoring{Interval: 5, Timeout: 1, Retries: 1, Target: "127.0.0.1:1"}}
	manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
	require.False(t, manager.Capture(true, false, nil))
	status := waitState(t, manager, "AWG", StateReconnecting)
	require.Contains(t, status.Message, "reconnect attempt 1 of 1")
	status = waitState(t, manager, "AWG", StateFailed)
	require.Contains(t, status.Message, "exited")
	require.Equal(t, -1, status.Latency)
	require.Contains(t, strings.Join(manager.Log("AWG"), "\n"), "boom")
	require.Contains(t, strings.Join(manager.Log("AWG"), "\n"),
		"starting '"+tools.AmneziaGo+"' on device 'proximity_a4'")
	require.Empty(t, manager.Counters())
	{
		outbound.AmneziaWG = &config.AmneziaWGOptions{Config: "broken"}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		status := waitState(t, manager, "AWG", StateFailed)
		require.Contains(t, status.Message, "wgconf.Parse failed")
	}
	{
		parsed, err := wgconf.Parse(sampleTunnel)
		require.NoError(t, err)
		driver, status := &amneziaDriver{device: "proximity_a4", parsed: parsed}, Status{}
		driver.describe(&status)
		require.Equal(t, "proximity_a4", status.Device)
		require.Equal(t, "10.8.1.2/32", status.Address)
		require.Equal(t, []string{"10.8.1.1"}, status.DNS)
		require.Equal(t, []string{"10.55.0.9/32", "10.66.0.0/16"}, status.Routes)
	}
}

func TestManager_ClearLog(t *testing.T) {
	tools, directory := makeTools(t)
	state := filepath.Join(directory, "state")
	manager, err := NewManager(Options{Resolve: sampleResolve, Directory: filepath.Join(directory,
		"run"), StateDir: state, Tools: tools})
	require.NoError(t, err)
	defer manager.Close()
	snapshot := config.Default()
	snapshot.Outbounds = []config.Outbound{{ID: "Off", Type: config.TypeHost, Slot: 2}}
	_, err = snapshot.AddInbound(config.Inbound{Type: config.TypeSocks5, ID: "Socks", Port: 7810,
		Anonymous: true}, time.Now())
	require.NoError(t, err)
	manager.Apply(snapshot.Outbounds, snapshot.Settings)
	manager.ApplyInbounds(snapshot)
	manager.book(outboundKey(2)).add("outbound line")
	manager.book(inboundKey(7810)).add("inbound line")
	require.Equal(t, []string{"outbound line"}, manager.Log("Off"))
	require.Equal(t, []string{"inbound line"}, manager.InboundLog("Socks"))
	require.NoError(t, manager.ClearLog("Off"))
	require.Empty(t, manager.Log("Off"))
	require.Equal(t, []string{"inbound line"}, manager.InboundLog("Socks"))
	require.NoError(t, manager.ClearInboundLog("Socks"))
	require.Empty(t, manager.InboundLog("Socks"))
	data, err := os.ReadFile(filepath.Join(state, logsDirectory, outboundKey(2)+logSuffix))
	require.NoError(t, err)
	require.Empty(t, data)
	require.EqualError(t, manager.ClearLog("missing"), "outbound 'missing' does not exist")
	require.EqualError(t, manager.ClearInboundLog("missing"), "inbound 'missing' does not exist")
}

func TestManager_Inbounds(t *testing.T) {
	tools, directory := makeTools(t)
	tools.OpenVPN = writeScript(t, directory, "openvpn-server", fakeServer)
	tools.Iptables = writeScript(t, directory, "iptables", fakeFirewall)
	tools.Forwarding = writeScript(t, directory, "ip_forward", "0\n")
	port, listener := makeInboundPort(t)
	slot := strconv.Itoa(port - config.InboundPortFirst)
	writeScript(t, directory, "port.txt", strconv.Itoa(port))
	home := filepath.Join(directory, "run", "inbound-"+slot)
	commands := makeManagement(t, filepath.Join(home, "management.sock"))
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory:  filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools, HostNetwork: true})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	snapshot := config.Default()
	_, err = snapshot.AddInbound(config.Inbound{Type: config.TypeSocks5, ID: "Socks", Port: port,
		Enabled: true, Statistics: true, Anonymous: true, Attempts: 1,
		Recovery: &config.Recovery{Delay: 60}}, time.Now())
	require.NoError(t, err)
	_, err = snapshot.AddInbound(config.Inbound{Type: config.TypeOpenVPN, ID: "VPN", Port: port - 1,
		Enabled: true, Statistics: true, Verbose: true, Users: []config.InboundUser{
			{Enabled: true, Name: "bob", Password: "builder"},
			{Enabled: true, Name: "eve", Password: "listener"}, {Name: "off", Password: "x"}},
		OpenVPN: &config.OpenVPNServer{Network: "10.77.2.0/24", OTP: true, Listen: "192.0.2.1",
			ClientToClient: true, RedirectGateway: true, Routes: []string{"192.168.10.0/24"}}},
		time.Now())
	require.NoError(t, err)
	snapshot.Inbounds = append(snapshot.Inbounds, config.Inbound{ID: "Blocked", Enabled: true,
		Type: config.TypeSocks5, Port: config.InboundPortFirst, Outbound: "gone", Anonymous: true},
		config.Inbound{ID: "Off", Type: config.TypeSocks5, Port: config.InboundPortFirst + 1})
	manager.ApplyInbounds(snapshot)
	require.Equal(t, StateFailed, manager.Inbounds()["Blocked"].State)
	require.Equal(t, "outbound 'gone' does not exist", manager.Inbounds()["Blocked"].Message)
	require.NotContains(t, manager.Inbounds(), "Off")
	status := waitInbound(t, manager, "Socks", StateUp)
	require.Equal(t, "127.0.0.1:"+strconv.Itoa(port), status.Address)
	require.Empty(t, status.Clients)
	status = waitInbound(t, manager, "VPN", StateUp)
	require.Equal(t, "proximity_i"+slot, status.Device)
	require.Equal(t, "10.77.2.1", status.Address)
	{
		text, err := os.ReadFile(filepath.Join(home, "server.conf"))
		require.NoError(t, err)
		server := snapshot.Inbounds[1].OpenVPN
		require.True(t, strings.HasPrefix(string(text), "port "+strconv.Itoa(port-1)+
			"\nproto udp4\ndev proximity_i"+slot+"\ndev-type tun\ntopology subnet\n"+
			"server 10.77.2.0 255.255.255.0\nkeepalive 10 60\nmax-clients 32\nreneg-sec 3600\n"+
			"tun-mtu 1420\ndata-ciphers AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305\nauth SHA256\n"+
			"tls-version-min 1.2\ndh none\nremote-cert-tls client\nduplicate-cn\n"+
			"username-as-common-name\nscript-security 2\nauth-user-pass-verify "+
			"\"/usr/local/bin/proximity verify "+filepath.Join(home, "auth.json")+"\" via-file\n"+
			"auth-user-pass-optional\ntmp-dir "+home+"\nmanagement "+
			filepath.Join(home, "management.sock")+" unix\nverb 6\nlocal 192.0.2.1\n"+
			"auth-gen-token 0 external-auth\nauth-gen-token-secret "+
			filepath.Join(home, tokenFile)+"\n"+
			"explicit-exit-notify 1\nclient-to-client\n"+
			"push \"redirect-gateway def1\"\npush \"block-outside-dns\"\n"+
			"push \"route 192.168.10.0 255.255.255.0\"\npush \"dhcp-option DNS 198.18.0.2\"\n"+
			"peer-fingerprint "+certificateFingerprint(server.ClientCertificate)+"\n"+
			"<ca>\n"), string(text))
		require.Contains(t, string(text), "<key>\n"+server.Key+"</key>\n")
		require.NotContains(t, string(text), server.ClientKey)
		data, err := os.ReadFile(filepath.Join(home, "auth.json"))
		require.NoError(t, err)
		require.JSONEq(t, `{"anonymous":false,"otp_key":"`+server.OTPKey+`","users":[`+
			`{"name":"bob","password":"builder"},{"name":"eve","password":"listener"}]}`,
			string(data))
		arguments, err := os.ReadFile(filepath.Join(directory, "server.log"))
		require.NoError(t, err)
		require.Equal(t, "--config "+filepath.Join(home, "server.conf")+" --mark 51000\n",
			string(arguments))
		forwarding, err := os.ReadFile(tools.Forwarding)
		require.NoError(t, err)
		require.Equal(t, "1\n", string(forwarding))
		table := strconv.Itoa(InboundTable(port - config.InboundPortFirst))
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-4 route replace blackhole default metric 4294967295 "+
			"table "+table)
		require.Contains(t, string(calls), "-4 route replace 10.77.2.0/24 dev proximity_i"+slot+
			" table "+table)
		require.Contains(t, string(calls), "-4 rule add priority 5140 from 10.77.2.0/24 lookup "+
			table)
		rules, err := os.ReadFile(filepath.Join(directory, "iptables.log"))
		require.NoError(t, err)
		require.Contains(t, string(rules), "-w 5 -D FORWARD -i proximity_i9 -j ACCEPT")
		require.NotContains(t, string(rules), "docker0 -j ACCEPT\n-w 5 -D")
		require.Contains(t, string(rules), "-w 5 -I FORWARD 1 -i proximity_i"+slot+" -j ACCEPT")
		require.Contains(t, string(rules), "-w 5 -I FORWARD 1 -o proximity_i"+slot+" -j ACCEPT")
		key, err := os.ReadFile(filepath.Join(home, tokenFile))
		require.NoError(t, err)
		require.Equal(t, tokenKey(server.Key+"\n"+server.OTPKey), string(key))
		require.NotEqual(t, string(key), tokenKey(server.Key+"\n"))
		decoded, rest := pem.Decode(key)
		require.Empty(t, rest)
		require.Equal(t, "OpenVPN auth-token server key", decoded.Type)
		require.Len(t, decoded.Bytes, 128)
		require.Contains(t, string(rules), "-w 5 -I FORWARD 1 -i proximity_i"+slot+
			" -p icmp -j REJECT --reject-with icmp-admin-prohibited")
		require.Contains(t, strings.Join(manager.InboundLog("VPN"), "\n"),
			"ping of clients to other hosts is rejected: it cannot follow the routes")
		require.Contains(t, string(rules), "-w 5 -N PROXIMITY-STAT-IN")
		require.Contains(t, string(rules), "-w 5 -A PROXIMITY-STAT-OUT -p udp --sport "+
			strconv.Itoa(port))
		require.Contains(t, string(rules), "-w 5 -I OUTPUT 1 -j PROXIMITY-STAT-OUT")
	}
	{
		require.Eventually(t, func() bool {
			return len(manager.Inbounds()["VPN"].Clients) == 2
		}, 10*time.Second, 20*time.Millisecond)
		require.Equal(t, []InboundClient{{Name: "bob", Address: "192.0.2.5:5000",
			Virtual: "10.77.2.2", Upload: 100, Download: 200, Since: 1700000000},
			{Name: "anonymous", Address: "192.0.2.6:5001", Virtual: "10.77.2.3", Upload: 1,
				Download: 2, Since: 1700000001}}, manager.Inbounds()["VPN"].Clients)
		require.Equal(t, map[string][2]int64{"Socks": {350, 7000}}, manager.InboundCounters())
		log := strings.Join(manager.InboundLog("VPN"), "\n")
		require.Contains(t, log, "Initialization Sequence Completed")
		require.Contains(t, log, "client certificate is rejected: it is not the current client "+
			"certificate of this inbound, export the client configuration again")
		require.NotContains(t, log, "MANAGEMENT: unix")
		require.Contains(t, log, "debug: protocol udp, listen '192.0.2.1', network '10.77.2.0/24', "+
			"verbosity 6")
		require.Contains(t, log, "debug: state connecting -> up")
		require.Contains(t, log, "debug: client 'bob' connected from 192.0.2.5:5000 as 10.77.2.2")
		require.Empty(t, manager.InboundLog("missing"))
		manager.TraceInbound("VPN", []string{"connection traced"})
		manager.TraceInbound("Socks", []string{"connection hidden"})
		require.Contains(t, strings.Join(manager.InboundLog("VPN"), "\n"), "debug: connection traced")
		socks := strings.Join(manager.InboundLog("Socks"), "\n")
		require.NotContains(t, socks, "debug:")
		require.Equal(t, 3000, manager.book(inboundKey(port-1)).limit)
		require.Equal(t, 300, manager.book(inboundKey(port)).limit)
	}
	{
		renamed := snapshot.Clone()
		renamed.Inbounds[1].ID, renamed.Inbounds[1].Anonymous = "Office", true
		renamed.Inbounds[1].Users = renamed.Inbounds[1].Users[1:]
		manager.ApplyInbounds(renamed)
		require.Equal(t, StateUp, manager.Inbounds()["Office"].State)
		require.NotContains(t, manager.Inbounds(), "VPN")
		data, err := os.ReadFile(filepath.Join(home, "auth.json"))
		require.NoError(t, err)
		require.Contains(t, string(data), `"anonymous":true`)
		require.NotContains(t, string(data), "bob")
		require.Contains(t, commands(), "client-kill 0 HALT")
		require.NotContains(t, commands(), "kill bob")
		log := strings.Join(manager.InboundLog("Office"), "\n")
		require.Contains(t, log, "users are updated: 1 enabled, anonymous connections allowed: true")
		require.Contains(t, log, "sessions of 'bob': SUCCESS: client-kill command succeeded")
		count := len(commands())
		manager.ApplyInbounds(renamed)
		renamed.Inbounds[1].Anonymous = false
		manager.ApplyInbounds(renamed)
		require.Equal(t, []string{"status 2", "client-kill 1 HALT", "client-kill 2 HALT"},
			commands()[count:])
		users := renamed.Inbounds[1].Users
		renamed.Inbounds[1].Users = append(slices.Clone(users), config.InboundUser{Enabled: true,
			Name: "zed", Password: "sleeper"})
		manager.ApplyInbounds(renamed)
		count = len(commands())
		renamed.Inbounds[1].Users = users
		manager.ApplyInbounds(renamed)
		require.Equal(t, []string{"status 2", "kill zed"}, commands()[count:])
		require.EqualError(t, manager.ControlInbound("VPN", ActionRestart),
			"inbound 'VPN' does not exist")
		require.NoError(t, manager.ControlInbound("Office", ActionRestart))
		waitInbound(t, manager, "Office", StateUp)
		data, err = os.ReadFile(filepath.Join(home, "auth.json"))
		require.NoError(t, err)
		require.JSONEq(t, `{"anonymous":false,"otp_key":"`+snapshot.Inbounds[1].OpenVPN.OTPKey+
			`","users":[{"name":"eve","password":"listener"}]}`, string(data))
		require.Eventually(t, func() bool {
			return strings.Contains(strings.Join(manager.InboundLog("Office"), "\n"),
				"management interface is not available")
		}, 10*time.Second, 20*time.Millisecond)
		require.Empty(t, manager.Inbounds()["Office"].Clients)
	}
	{
		streamed := snapshot.Clone()
		streamed.Inbounds[1].ID, streamed.Inbounds[1].Users = "Office", streamed.Inbounds[1].Users[1:]
		streamed.Inbounds[1].OpenVPN.Protocol = config.ProtocolTCP
		manager.ApplyInbounds(streamed)
		require.Eventually(t, func() bool {
			text, err := os.ReadFile(filepath.Join(home, "server.conf"))
			return err == nil && strings.Contains(string(text), "\nproto tcp4-server\n")
		}, 10*time.Second, 20*time.Millisecond)
		text, err := os.ReadFile(filepath.Join(home, "server.conf"))
		require.NoError(t, err)
		require.Contains(t, string(text), "auth-gen-token 0 external-auth\nauth-gen-token-secret "+
			filepath.Join(home, tokenFile)+"\ntcp-nodelay\nclient-to-client\n")
		require.NotContains(t, string(text), "explicit-exit-notify")
		waitInbound(t, manager, "Office", StateUp)
	}
	{
		require.NoError(t, listener.Close())
		status := waitInbound(t, manager, "Socks", StateFailed)
		require.Equal(t, "router does not listen on port ("+strconv.Itoa(port)+")", status.Message)
		require.Contains(t, strings.Join(manager.InboundLog("Socks"), "\n"),
			"all 1 connection attempts failed")
		writeScript(t, directory, "fail", "x")
		changed := snapshot.Clone()
		changed.Inbounds[1].OpenVPN.MaxClients, changed.Inbounds[1].ReconnectTimeout = 5, 1
		changed.Inbounds[0].Statistics = false
		manager.ApplyInbounds(changed)
		require.Eventually(t, func() bool {
			return strings.HasPrefix(manager.Inbounds()["VPN"].Message, "Options error: boom: process")
		}, 10*time.Second, 20*time.Millisecond)
		require.Contains(t, []string{StateReconnecting, StateFailed}, manager.Inbounds()["VPN"].State)
		require.Empty(t, manager.InboundCounters())
		changed.Inbounds[1].Enabled = false
		manager.ApplyInbounds(changed)
		require.NotContains(t, manager.Inbounds(), "VPN")
		require.NoDirExists(t, home)
		rules, err := os.ReadFile(filepath.Join(directory, "iptables.log"))
		require.NoError(t, err)
		require.Contains(t, string(rules), "-w 5 -X PROXIMITY-STAT-IN")
	}
}

func TestManager_Names(t *testing.T) {
	tools, directory := makeTools(t)
	target := makeListener(t)
	_, port, err := net.SplitHostPort(target)
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	answers := map[string]string{"proxy.example.com": "127.0.0.1", "down.example.com": "127.0.0.1",
		"fake.example.com": "198.18.0.7", "vpn.example.com": sampleAddress}
	manager, err := NewManager(Options{Directory: filepath.Join(directory, "run"), Tools: tools,
		Resolve: func(_ context.Context, host string) ([]string, error) {
			if answers[host] == "" {
				return nil, fmt.Errorf("failed to resolve '%v': no such name", host)
			}
			return []string{answers[host]}, nil
		}})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	tunnel := strings.ReplaceAll(sampleTunnel, "127.0.0.1:51820", "vpn.example.com:51820")
	monitoring := &config.Monitoring{Interval: 5, Timeout: 1, Retries: 1, Target: "127.0.0.1:1"}
	outbounds := []config.Outbound{
		{ID: "S", Type: config.TypeSocks5, Enabled: true, Slot: 1, Verbose: true,
			Socks5: &config.Socks5Options{Server: "proxy.example.com", Port: number}},
		{ID: "D", Type: config.TypeHTTP, Enabled: true, Slot: 2,
			HTTP: &config.HTTPOptions{Server: "down.example.com", Port: 1}},
		{ID: "F", Type: config.TypeSocks5, Enabled: true, Slot: 3,
			Socks5: &config.Socks5Options{Server: "fake.example.com", Port: number}},
		{ID: "M", Type: config.TypeSocks5, Enabled: true, Slot: 4,
			Socks5: &config.Socks5Options{Server: "missing.example.com", Port: number}},
		{ID: "A", Type: config.TypeAmneziaWG, Enabled: true, Slot: 5, Attempts: 1, ReconnectTimeout: 1,
			AmneziaWG: &config.AmneziaWGOptions{Config: tunnel}, Monitoring: monitoring,
			Recovery: &config.Recovery{}},
		{ID: "B", Type: config.TypeAmneziaWG, Enabled: true, Slot: 6, Attempts: 1, ReconnectTimeout: 1,
			AmneziaWG: &config.AmneziaWGOptions{Config: strings.ReplaceAll(sampleTunnel,
				"127.0.0.1:51820", "other.example.com:51820")}, Monitoring: monitoring,
			Recovery: &config.Recovery{}},
		{ID: "V", Type: config.TypeOpenVPN, Enabled: true, Slot: 7, Attempts: 1, ReconnectTimeout: 1,
			OpenVPN:    &config.OpenVPNOptions{Config: "client\nremote other.example.com 1194\n"},
			Monitoring: monitoring, Recovery: &config.Recovery{}}}
	manager.Apply(outbounds, config.Settings{ProbeTarget: target, CheckInterval: 5,
		ConnectivityChecks: true})
	status := waitState(t, manager, "S", StateUp)
	require.Equal(t, "proxy.example.com:"+port, status.Gateway)
	require.Contains(t, strings.Join(manager.Log("S"), "\n"),
		"debug: 'proxy.example.com:"+port+"' is resolved to '"+target+"'")
	status = waitState(t, manager, "D", StateFailed)
	require.Contains(t, status.Message,
		"failed to check 'down.example.com:1': failed to connect to '127.0.0.1:1'")
	status = waitState(t, manager, "F", StateFailed)
	require.Equal(t, "name 'fake.example.com' has no usable address (198.18.0.7)", status.Message)
	status = waitState(t, manager, "M", StateFailed)
	require.Equal(t, "failed to resolve 'missing.example.com': no such name", status.Message)
	{
		waitState(t, manager, "A", StateFailed)
		require.Contains(t, strings.Join(manager.Log("A"), "\n"),
			"'vpn.example.com' is resolved to "+sampleAddress)
		status := waitState(t, manager, "B", StateFailed)
		require.Equal(t, "failed to resolve Endpoint 'other.example.com': failed to resolve "+
			"'other.example.com': no such name", status.Message)
		status = waitState(t, manager, "V", StateFailed)
		require.Equal(t, "failed to resolve 'other.example.com': no such name", status.Message)
		require.Contains(t, strings.Join(manager.Log("V"), "\n"),
			"remote 'other.example.com' is skipped: failed to resolve")
	}
}

func TestManager_OpenVPN(t *testing.T) {
	tools, directory := makeTools(t)
	changes := atomic.Int32{}
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory:  filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools, Changed: func() {
			changes.Add(1)
		}})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	outbound := config.Outbound{ID: "VPN", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
		Attempts: 1, ReconnectTimeout: 1, OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN,
			Username: "alice", Password: "wonderland", OTPKey: "GEZDGNBVGY3TQOJQ",
			OTPMode: config.OTPAppend},
		Monitoring: &config.Monitoring{Enabled: true, Interval: 5, Timeout: 1, Retries: 1,
			Target: "127.0.0.1:1"}}
	manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
	status := waitState(t, manager, "VPN", StateUp)
	require.Equal(t, "proximity_o7", status.Device)
	require.Equal(t, "10.88.0.1", status.Gateway)
	require.Equal(t, "10.88.0.2", status.Address)
	require.Equal(t, []string{"10.99.0.1"}, status.DNS)
	require.Equal(t, []string{"corp.test"}, status.Domains)
	require.Equal(t, []string{"10.99.0.0/16", "10.77.1.5/32", "fd00::/64"}, status.Routes)
	require.Equal(t, "absent", status.Link)
	require.Positive(t, changes.Load())
	{
		data, err := os.ReadFile(filepath.Join(directory, "run", "answers.txt"))
		require.NoError(t, err)
		parts := strings.Split(strings.TrimSpace(string(data)), "|")
		require.Len(t, parts, 3)
		require.Equal(t, "alice", parts[0])
		require.Regexp(t, `^wonderland\d{6}$`, parts[1])
		require.Regexp(t, `^\d{6}$`, parts[2])
		text, err := os.ReadFile(filepath.Join(directory, "run", "openvpn-7", "client.ovpn"))
		require.NoError(t, err)
		require.Equal(t, "client\nremote 203.0.113.9 1194 udp\n\nauth-user-pass\n", string(text))
		log := strings.Join(manager.Log("VPN"), "\n")
		require.Contains(t, log, "Enter Auth Username:")
		require.Contains(t, log, "CHALLENGE: Enter OTP")
		require.Contains(t, log, "Initialization Sequence Completed")
		require.Contains(t, log, "skipped route '10.1.0.0'")
		require.NotContains(t, log, "wonderland")
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-4 route flush table 51005")
		require.Contains(t, string(calls), "-4 rule add priority 5100 fwmark 51007 lookup 51007")
		require.Contains(t, string(calls), "-4 rule add priority 5100 from 10.88.0.2/32 lookup 51007")
		require.NotContains(t, string(calls), "rule add priority 5150")
		arguments, err := os.ReadFile(filepath.Join(directory, "openvpn.log"))
		require.NoError(t, err)
		require.Contains(t, string(arguments), "--dev proximity_o7 --dev-type tun --route-noexec")
		require.Contains(t, string(arguments), "--verb 3 ")
		require.NotContains(t, string(arguments), "--mark")
		require.NotContains(t, string(arguments), "--socket-flags")
		require.NotContains(t, log, "debug:")
		require.Contains(t, string(calls), "-4 route replace default dev proximity_o7 table 51007")
		require.Contains(t, string(calls),
			"-4 route replace 10.99.0.0/16 via 10.88.0.1 dev proximity_o7 table 51007")
		require.Contains(t, string(calls), "-6 route replace fd00::/64 dev proximity_o7 table 51007")
	}
	{
		require.Eventually(t, func() bool {
			status := manager.Status()["VPN"]
			return status.Checked != 0 && status.Latency >= 0
		}, 10*time.Second, 50*time.Millisecond)
		require.Empty(t, manager.Counters())
	}
	{
		require.NoError(t, os.WriteFile(tools.Ping, []byte("#!/bin/bash\nexit 1\n"), 0o700))
		status := waitState(t, manager, "VPN", StateFailed)
		require.Contains(t, status.Message, "monitoring failed after 2 attempts")
		require.Contains(t, status.Message, "failed to ping gateway '10.88.0.1'")
		require.Empty(t, status.Address)
	}
	{
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "denied",
			Password: "x", OTPMode: config.OTPChallenge}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		status := waitState(t, manager, "VPN", StateFailed)
		require.Equal(t, "authentication failed", status.Message)
	}
	{
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "bob",
			OTPMode: config.OTPChallenge}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		status := waitState(t, manager, "VPN", StateFailed)
		require.Equal(t, "password is required by server", status.Message)
	}
	{
		stubborn := writeScript(t, directory, "stubborn", "x")
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Password: "x",
			OTPMode: config.OTPChallenge}
		started := time.Now()
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		status := waitState(t, manager, "VPN", StateFailed)
		require.Equal(t, "user name is required by server", status.Message)
		require.Less(t, time.Since(started), 10*time.Second)
		require.NoError(t, os.Remove(stubborn))
	}
	{
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "locked",
			Password: "x", KeyPassword: "wrong", OTPMode: config.OTPChallenge}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		status := waitState(t, manager, "VPN", StateFailed)
		require.Equal(t, "invalid private key password", status.Message)
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "untrusted",
			Password: "x", OTPMode: config.OTPChallenge}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		require.Eventually(t, func() bool {
			return strings.HasPrefix(manager.Status()["VPN"].Message, "server certificate")
		}, 15*time.Second, 20*time.Millisecond)
		status = manager.Status()["VPN"]
		require.Equal(t, StateFailed, status.State)
		require.Equal(t, "server certificate is rejected: depth=1, error=self-signed certificate: "+
			"CN=Evil CA", status.Message)
	}
	{
		require.NoError(t, os.WriteFile(tools.Ping, []byte("#!/bin/bash\nexit 0\n"), 0o700))
		outbound.Verbose = true
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "alice",
			Password: "wonderland", OTPKey: "GEZDGNBVGY3TQOJQ", OTPMode: config.OTPAppend}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		waitState(t, manager, "VPN", StateUp)
		arguments, err := os.ReadFile(filepath.Join(directory, "openvpn.log"))
		require.NoError(t, err)
		require.Contains(t, string(arguments), "--verb 6 ")
		require.NotContains(t, string(arguments), "--socket-flags")
		outbound.OpenVPN.Config = "client\nproto tcp-client\nremote vpn.example.com 443\n"
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		require.Eventually(t, func() bool {
			arguments, err := os.ReadFile(filepath.Join(directory, "openvpn.log"))
			return err == nil && strings.Contains(string(arguments), " --socket-flags TCP_NODELAY")
		}, 15*time.Second, 20*time.Millisecond)
		waitState(t, manager, "VPN", StateUp)
		log := strings.Join(manager.Log("VPN"), "\n")
		require.Contains(t, log, "debug: type openvpn, attempts 1, reconnect timeout 1 s")
		require.Contains(t, log, "debug: monitoring every 5 s, timeout 1 s, retries 1, "+
			"target '127.0.0.1:1'")
		require.Contains(t, log, "debug: routing table 51007, command: ")
		require.Contains(t, log, "debug: state connecting -> up")
		require.NotContains(t, log, "wonderland")
	}
}

func TestManager_Capture(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory:  filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools, HostNetwork: true})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	manager.capture.device = "proximity_missing"
	require.False(t, manager.Capture(true, true, nil))
	calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
	require.NoError(t, err)
	for _, line := range []string{"-4 rule add priority 5150 fwmark 51000 lookup main",
		"-4 rule add priority 5155 from 0.0.0.0/32 iif lo ipproto 17 dport 53 lookup 51000",
		"-4 rule add priority 5155 from 0.0.0.0/32 iif lo ipproto 6 dport 53 lookup 51000",
		"-4 rule add priority 5155 from 198.18.0.0/30 iif lo ipproto 17 dport 53 lookup 51000",
		"-4 rule add priority 5155 from 198.18.0.0/30 iif lo ipproto 6 dport 53 lookup 51000",
		"-4 rule add priority 5160 lookup main suppress_prefixlength 0",
		"-4 rule add priority 5170 from 0.0.0.0/32 iif lo lookup 51000",
		"-4 rule add priority 5170 from 198.18.0.0/30 iif lo lookup 51000",
		"-6 rule add priority 5155 from ::/128 iif lo ipproto 17 dport 53 lookup 51000",
		"-6 rule add priority 5155 from fdfe:dcba:9876::/126 iif lo ipproto 6 dport 53 lookup 51000",
		"-6 rule add priority 5170 from ::/128 iif lo lookup 51000",
		"-6 rule add priority 5170 from fdfe:dcba:9876::/126 iif lo lookup 51000"} {
		require.Contains(t, string(calls), line+"\n")
	}
	require.NotContains(t, string(calls), "route replace default dev")
	{
		manager.capture.device = "lo"
		require.True(t, manager.Capture(true, true, nil))
		require.True(t, manager.Capture(true, true, nil))
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(calls),
			"-4 route replace default dev lo table 51000\n"))
		require.Equal(t, 1, strings.Count(string(calls),
			"-6 route replace default dev lo table 51000\n"))
		require.True(t, manager.Capture(true, false, nil))
		calls, err = os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Equal(t, 2, strings.Count(string(calls),
			"-4 route replace default dev lo table 51000\n"))
	}
	{
		tools.Iptables = writeScript(t, directory, "iptables", fakeFirewall)
		tools.Ip6tables = writeScript(t, directory, "ip6tables",
			"#!/bin/bash\necho \"$*\" >> \"$(dirname \"$0\")/ip6tables.log\"\n")
		manager.options.Tools = tools
		ping := []PingRule{{Prefix: netip.MustParsePrefix("1.1.1.0/24")},
			{Prefix: netip.MustParsePrefix("2001:db8::/32"), Allow: true}, {Allow: true}}
		require.True(t, manager.Capture(true, true, ping))
		require.True(t, manager.Capture(true, true, ping))
		calls, err := os.ReadFile(filepath.Join(directory, "iptables.log"))
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(calls), "-w 5 -N PROXIMITY-PING\n"))
		require.Contains(t, string(calls), "-w 5 -A PROXIMITY-PING -d 1.1.1.0/24 -j DROP\n"+
			"-w 5 -A PROXIMITY-PING -j RETURN\n-w 5 -I OUTPUT 1 -m mark --mark 51000 -p icmp "+
			"--icmp-type echo-request -j PROXIMITY-PING\n")
		require.NotContains(t, string(calls), "2001:db8")
		calls, err = os.ReadFile(filepath.Join(directory, "ip6tables.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-w 5 -A PROXIMITY-PING -d 2001:db8::/32 -j RETURN\n"+
			"-w 5 -A PROXIMITY-PING -j RETURN\n-w 5 -I OUTPUT 1 -m mark --mark 51000 -p "+
			"ipv6-icmp --icmpv6-type echo-request -j PROXIMITY-PING\n")
		require.NotContains(t, string(calls), "1.1.1.0")
		require.True(t, manager.Capture(true, false, ping))
		require.True(t, manager.Capture(true, false, nil))
		calls, err = os.ReadFile(filepath.Join(directory, "iptables.log"))
		require.NoError(t, err)
		require.Equal(t, 2, strings.Count(string(calls), "-w 5 -N PROXIMITY-PING\n"))
		require.Equal(t, 3, strings.Count(string(calls), "-w 5 -X PROXIMITY-PING\n"))
	}
	{
		ping := []PingRule{{Prefix: netip.MustParsePrefix("10.1.0.0/16"), Allow: true},
			{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Table: 51007},
			{Prefix: netip.MustParsePrefix("2001:db8::/32"), Table: 51007},
			{Prefix: netip.MustParsePrefix("192.0.2.0/24")}, {Table: 51008}}
		require.True(t, manager.Capture(true, true, ping))
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-4 rule add priority 5165 ipproto 1 to 10.1.0.0/16 "+
			"lookup 51000\n-4 rule add priority 5165 ipproto 1 to 10.0.0.0/8 lookup 51007\n"+
			"-4 rule add priority 5165 ipproto 1 to 192.0.2.0/24 lookup 51000\n"+
			"-4 rule add priority 5165 ipproto 1 to 198.18.0.0/16 lookup 51000\n"+
			"-4 rule add priority 5166 ipproto 1 lookup 51008\n")
		require.Contains(t, string(calls), "-6 rule add priority 5165 ipproto 58 to 2001:db8::/32 "+
			"lookup 51007\n-6 rule add priority 5166 ipproto 58 lookup 51008\n")
		require.NotContains(t, string(calls), "ipproto 58 to 10.")
		firewall, err := os.ReadFile(filepath.Join(directory, "iptables.log"))
		require.NoError(t, err)
		require.Contains(t, string(firewall), "-w 5 -A PROXIMITY-PING -d 10.0.0.0/8 -j DROP\n")
		require.Contains(t, string(firewall), "-w 5 -A POSTROUTING -t nat -s 198.18.0.0/30 -o "+
			"proximity_o+ -p icmp -j MASQUERADE\n-w 5 -A POSTROUTING -t nat -s 198.18.0.0/30 -o "+
			"proximity_a+ -p icmp -j MASQUERADE\n")
		firewall, err = os.ReadFile(filepath.Join(directory, "ip6tables.log"))
		require.NoError(t, err)
		require.Contains(t, string(firewall), "-w 5 -A POSTROUTING -t nat -s "+
			"fdfe:dcba:9876::/126 -o proximity_a+ -p ipv6-icmp -j MASQUERADE\n")
		require.False(t, manager.Capture(false, false, ping))
		calls, err = os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-6 rule del priority 5165\n-6 rule del priority 5166\n")
		firewall, err = os.ReadFile(filepath.Join(directory, "iptables.log"))
		require.NoError(t, err)
		require.Contains(t, string(firewall), "-w 5 -D POSTROUTING -t nat -s 198.18.0.0/30 -o "+
			"proximity_a+ -p icmp -j MASQUERADE\n")
	}
	{
		outbound := config.Outbound{ID: "VPN", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
			OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "alice",
				Password: "wonderland", OTPKey: "GEZDGNBVGY3TQOJQ", OTPMode: config.OTPChallenge},
			ReconnectTimeout: 1, Monitoring: &config.Monitoring{Interval: 5, Timeout: 1, Retries: 1,
				Target: "127.0.0.1:1"}}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5, Capture: true})
		waitState(t, manager, "VPN", StateUp)
		arguments, err := os.ReadFile(filepath.Join(directory, "openvpn.log"))
		require.NoError(t, err)
		require.Contains(t, string(arguments), "--mark 51000")
		since := manager.Status()["VPN"].Since
		outbound.ID = "Office"
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5, Capture: true})
		require.NotContains(t, manager.Status(), "VPN")
		require.Equal(t, StateUp, manager.Status()["Office"].State)
		require.Equal(t, since, manager.Status()["Office"].Since)
		require.NotEmpty(t, manager.Log("Office"))
		require.Empty(t, manager.Log("VPN"))
		require.EqualError(t, manager.Control("VPN", ActionRestart), "outbound 'VPN' does not exist")
		require.NoError(t, manager.Control("Office", ActionRestart))
		waitState(t, manager, "Office", StateUp)
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		waitState(t, manager, "Office", StateUp)
		arguments, err = os.ReadFile(filepath.Join(directory, "openvpn.log"))
		require.NoError(t, err)
		require.NotContains(t, string(arguments), "--mark")
	}
	{
		require.False(t, manager.Capture(false, false, nil))
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-4 rule del priority 5155\n")
		require.Contains(t, string(calls), "-4 rule del priority 5170\n")
		require.Contains(t, string(calls), "-6 route flush table 51000\n")
	}
	{
		plain, err := NewManager(Options{Resolve: sampleResolve,
			Directory: filepath.Join(directory, "plain"), Tools: tools})
		require.NoError(t, err)
		defer plain.Close()
		plain.capture.device = "lo"
		require.False(t, plain.Capture(true, false, nil))
	}
}

func TestManager_HostAttempts(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory: filepath.Join(directory, "run"), Tools: tools})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	outbound := config.Outbound{ID: "M", Type: config.TypeHost, Enabled: true, Slot: 5,
		Attempts: 2, ReconnectTimeout: 1,
		Host: &config.HostOptions{Mode: config.HostInterface, Interface: "missing0"}}
	settings := config.Settings{ProbeTarget: "127.0.0.1:1", CheckInterval: 5,
		ConnectivityChecks: true}
	manager.Apply([]config.Outbound{outbound}, settings)
	status := waitState(t, manager, "M", StateReconnecting)
	require.Equal(t, "reconnect attempt 1 of 1: interface 'missing0' does not exist",
		status.Message)
	status = waitState(t, manager, "M", StateFailed)
	require.Equal(t, "interface 'missing0' does not exist", status.Message)
	{
		outbound.Host, outbound.Attempts = &config.HostOptions{Mode: config.HostDefault}, 1
		manager.Apply([]config.Outbound{outbound}, settings)
		status := waitState(t, manager, "M", StateFailed)
		require.Contains(t, status.Message, "failed to connect to '127.0.0.1:1'")
		settings.ProbeTarget = makeListener(t)
		manager.Apply([]config.Outbound{outbound}, settings)
		status = waitState(t, manager, "M", StateUp)
		require.NotZero(t, status.Checked)
		require.GreaterOrEqual(t, status.Latency, 0)
	}
	{
		settings.ProbeTarget, settings.ConnectivityChecks = "127.0.0.1:1", false
		manager.Apply([]config.Outbound{outbound}, settings)
		status := waitState(t, manager, "M", StateUp)
		require.Zero(t, status.Checked)
		require.Equal(t, -1, status.Latency)
	}
}

func TestManager_Proxies(t *testing.T) {
	tools, directory := makeTools(t)
	target := makeListener(t)
	host, port, err := net.SplitHostPort(target)
	require.NoError(t, err)
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory: filepath.Join(directory, "run"), Tools: tools})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	settings := config.Settings{ProbeTarget: target, CheckInterval: 5, ConnectivityChecks: true}
	outbounds := []config.Outbound{
		{ID: "S", Type: config.TypeSocks5, Enabled: true, Slot: 1, Verbose: true,
			Socks5: &config.Socks5Options{Server: host, Port: number}},
		{ID: "H", Type: config.TypeHTTP, Enabled: true, Slot: 2,
			HTTP: &config.HTTPOptions{Server: "127.0.0.1", Port: 1}},
		{ID: "D", Type: config.TypeHost, Enabled: true, Slot: 3, Attempts: 3, ReconnectTimeout: 1,
			Host: &config.HostOptions{Mode: config.HostDefault}},
		{ID: "L", Type: config.TypeHost, Enabled: true, Slot: 6, Attempts: 3, ReconnectTimeout: 1,
			Host: &config.HostOptions{Mode: config.HostInterface, Interface: "lo",
				Gateway: "127.0.0.1"}},
		{ID: "X", Type: config.TypeHost, Slot: 4, Host: &config.HostOptions{}}}
	manager.Apply(outbounds, settings)
	status := waitState(t, manager, "S", StateUp)
	require.Equal(t, target, status.Gateway)
	require.NotZero(t, status.Checked)
	require.GreaterOrEqual(t, status.Latency, 0)
	waitState(t, manager, "D", StateUp)
	status = waitState(t, manager, "H", StateFailed)
	require.Contains(t, status.Message, "failed to connect to '127.0.0.1:1'")
	require.Eventually(t, func() bool {
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		return err == nil && strings.Contains(string(calls),
			"rule add priority 5100 from 127.0.0.1/32 lookup 51006")
	}, 5*time.Second, 20*time.Millisecond)
	require.NotContains(t, manager.Status(), "X")
	require.Contains(t, strings.Join(manager.Log("H"), "\n"), "connectivity check 1 of 1 failed")
	require.NotContains(t, strings.Join(manager.Log("H"), "\n"), "debug:")
	require.Empty(t, manager.Log("missing"))
	{
		log := strings.Join(manager.Log("S"), "\n")
		require.Contains(t, log, "debug: connectivity checks true, every 5 s, probe target '"+
			target+"'")
		require.Contains(t, log, "debug: proxy server '"+target+"'")
		require.Contains(t, log, "debug: checking TCP connection to proxy server '"+target+"'")
		require.Contains(t, log, "debug: connectivity check 1 of 1 passed in ")
		require.Contains(t, log, "debug: state connecting -> up")
		lines := []string{}
		for index := range maxTracedLines + 3 {
			lines = append(lines, fmt.Sprintf("connection %v", index))
		}
		manager.TraceOutbound("S", lines)
		manager.TraceOutbound("H", lines)
		log = strings.Join(manager.Log("S"), "\n")
		require.Contains(t, log, fmt.Sprintf("debug: connection %v", maxTracedLines-1))
		require.NotContains(t, log, fmt.Sprintf("debug: connection %v", maxTracedLines))
		require.Contains(t, log, "debug: 3 more connections are not shown")
		require.NotContains(t, strings.Join(manager.Log("H"), "\n"), "debug: connection")
	}
	{
		limited := settings
		limited.LogLines, limited.VerboseLogLines = 10, 15
		manager.Apply(outbounds, limited)
		require.Len(t, manager.Log("S"), 15)
		require.LessOrEqual(t, len(manager.Log("H")), 10)
		manager.TraceOutbound("S", []string{"after limit"})
		require.Len(t, manager.Log("S"), 15)
		require.Equal(t, 15, manager.logLimit(true))
		manager.Apply(outbounds, settings)
		require.Equal(t, maxVerboseLines, manager.logLimit(true))
		require.Equal(t, maxLogLines, manager.logLimit(false))
	}
	require.Empty(t, manager.Counters())
	{
		require.NoError(t, manager.Control("S", ActionRestart))
		waitState(t, manager, "S", StateUp)
		require.EqualError(t, manager.Control("missing", ActionRestart),
			"outbound 'missing' does not exist")
		require.NoError(t, manager.Control("S", ActionStop))
		require.Equal(t, StateStopped, manager.Status()["S"].State)
		require.NoError(t, manager.Control("S", ActionStart))
		waitState(t, manager, "S", StateUp)
	}
	{
		outbounds[0].Enabled, outbounds[2].Statistics = false, true
		manager.Apply(outbounds[:3], settings)
		require.NotContains(t, manager.Status(), "S")
		require.NotContains(t, manager.Status(), "L")
		require.Contains(t, manager.Status(), "D")
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls),
			"route replace default via 127.0.0.1 dev lo table 51006")
		require.Contains(t, string(calls), "-4 route flush table 51006")
	}
	{
		manager.Close()
		require.Empty(t, manager.runners)
		require.Equal(t, StateFailed, manager.Status()["H"].State)
	}
}

func TestManager_Recovery(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory:  filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	settings := config.Settings{CheckInterval: 5}
	outbound := config.Outbound{ID: "VPN", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
		Attempts: 1, ReconnectTimeout: 1, Verbose: true, OpenVPN: &config.OpenVPNOptions{
			Config: sampleOpenVPN, Username: "alice", Password: "wonderland",
			OTPKey: "GEZDGNBVGY3TQOJQ", OTPMode: config.OTPAppend},
		Recovery: &config.Recovery{Enabled: true, Delay: 1}}
	down := writeScript(t, directory, "down", "x")
	manager.Apply([]config.Outbound{outbound}, settings)
	status := waitState(t, manager, "VPN", StateReconnecting)
	require.True(t, strings.HasPrefix(status.Message, "recovery attempt 1: "))
	require.Eventually(t, func() bool {
		return strings.Contains(strings.Join(manager.Log("VPN"), "\n"), "recovery attempt 3 in 1s")
	}, 15*time.Second, 20*time.Millisecond)
	require.Contains(t, strings.Join(manager.Log("VPN"), "\n"),
		"debug: recovery true, delay 1 s, attempts 0")
	require.NoError(t, os.Remove(down))
	status = waitState(t, manager, "VPN", StateUp)
	require.Empty(t, status.Message)
	{
		writeScript(t, directory, "down", "x")
		outbound.ID, outbound.Recovery = "Limited", &config.Recovery{Enabled: true, Delay: 1,
			Attempts: 2}
		manager.Apply([]config.Outbound{outbound}, settings)
		status := waitState(t, manager, "Limited", StateReconnecting)
		require.True(t, strings.HasPrefix(status.Message, "recovery attempt 1 of 2: "))
		status = waitState(t, manager, "Limited", StateFailed)
		require.NotContains(t, status.Message, "recovery")
		log := strings.Join(manager.Log("Limited"), "\n")
		require.Contains(t, log, "recovery attempt 2 of 2 in 1s")
		require.Contains(t, log, "recovery is stopped after 2 attempts")
		outbound.ID, outbound.Recovery = "Off", &config.Recovery{Delay: 1}
		manager.Apply([]config.Outbound{outbound}, settings)
		waitState(t, manager, "Off", StateFailed)
		log = strings.Join(manager.Log("Off"), "\n")
		require.Contains(t, log, "debug: recovery false, delay 1 s, attempts 0")
		require.Equal(t, 1, strings.Count(log, "recovery is stopped"))
		require.NoError(t, os.Remove(down))
	}
	{
		count := strings.Count(strings.Join(manager.Log("Off"), "\n"), "recovery attempt")
		outbound.ID, outbound.Attempts = "Denied", 1
		outbound.OpenVPN = &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "denied",
			Password: "x", OTPKey: "GEZDGNBVGY3TQOJQ", OTPMode: config.OTPChallenge}
		outbound.Recovery = &config.Recovery{Enabled: true, Delay: 1}
		manager.Apply([]config.Outbound{outbound}, settings)
		status := waitState(t, manager, "Denied", StateFailed)
		require.Equal(t, "authentication failed", status.Message)
		log := strings.Join(manager.Log("Denied"), "\n")
		require.Contains(t, log, "connection failed after 1 of 1 attempts")
		require.Equal(t, count, strings.Count(log, "recovery attempt"))
		outbound.ID, outbound.Attempts, outbound.OpenVPN.OTPKey = "Fatal", 2, ""
		manager.Apply([]config.Outbound{outbound}, settings)
		status = waitState(t, manager, "Fatal", StateFailed)
		require.Equal(t, "authentication failed", status.Message)
		log = strings.Join(manager.Log("Fatal"), "\n")
		require.Contains(t, log, "connection failed after 1 of 2 attempts")
		require.Equal(t, count, strings.Count(log, "recovery attempt"))
	}
	{
		host := config.Outbound{ID: "H", Type: config.TypeHost, Enabled: true, Slot: 5, Attempts: 1,
			ReconnectTimeout: 1, Recovery: &config.Recovery{Enabled: true, Delay: 1, Attempts: 2},
			Host: &config.HostOptions{Mode: config.HostInterface, Interface: "missing0"}}
		manager.Apply([]config.Outbound{host}, config.Settings{ProbeTarget: "127.0.0.1:1",
			CheckInterval: 30, ConnectivityChecks: true})
		status := waitState(t, manager, "H", StateFailed)
		require.Equal(t, "interface 'missing0' does not exist", status.Message)
		require.Eventually(t, func() bool {
			return strings.Contains(strings.Join(manager.Log("H"), "\n"),
				"recovery is stopped after 2 attempts")
		}, 10*time.Second, 20*time.Millisecond)
		require.Equal(t, StateFailed, manager.Status()["H"].State)
	}
	{
		tools.OpenVPN = writeScript(t, directory, "openvpn-server", fakeServer)
		tools.Iptables = writeScript(t, directory, "iptables", fakeFirewall)
		tools.Forwarding = writeScript(t, directory, "ip_forward", "0\n")
		port, _ := makeInboundPort(t)
		server, err := NewManager(Options{Resolve: sampleResolve,
			Directory:  filepath.Join(directory, "server"),
			Executable: "/usr/local/bin/proximity", Tools: tools, HostNetwork: true})
		require.NoError(t, err)
		server.SetActive(true, config.Config{})
		defer server.Close()
		snapshot := config.Default()
		_, err = snapshot.AddInbound(config.Inbound{Type: config.TypeOpenVPN, ID: "Server",
			Port: port, Enabled: true, Attempts: 1, ReconnectTimeout: 1, Anonymous: true,
			Verbose: true, Recovery: &config.Recovery{Enabled: true, Delay: 1}}, time.Now())
		require.NoError(t, err)
		fail := writeScript(t, directory, "fail", "x")
		server.ApplyInbounds(snapshot)
		status := waitInbound(t, server, "Server", StateReconnecting)
		require.True(t, strings.HasPrefix(status.Message,
			"recovery attempt 1: Options error: boom"))
		require.Contains(t, strings.Join(server.InboundLog("Server"), "\n"),
			"debug: recovery true, delay 1 s, attempts 0")
		require.NoError(t, os.Remove(fail))
		status = waitInbound(t, server, "Server", StateUp)
		require.Empty(t, status.Message)
		writeScript(t, directory, "fail", "x")
		snapshot.Inbounds[0].Recovery = &config.Recovery{Enabled: true, Delay: 1, Attempts: 1}
		server.ApplyInbounds(snapshot)
		status = waitInbound(t, server, "Server", StateFailed)
		require.True(t, strings.HasPrefix(status.Message, "Options error: boom"))
		log := strings.Join(server.InboundLog("Server"), "\n")
		require.Contains(t, log, "recovery attempt 1 of 1 in 1s")
		require.Contains(t, log, "recovery is stopped after 1 attempts")
	}
}

func TestManager_RejectedCode(t *testing.T) {
	tools, directory := makeTools(t)
	tools.Faketime = "/lib/libfaketime.so.1"
	// NOTE: The clock stands 3 seconds before a new one-time code, so the wait is short.
	offset := time.Duration(27-time.Now().Unix()%30) * time.Second
	timeClock := clock.NewWith(filepath.Join(directory, "clock.json"),
		func(_ context.Context, _ string, _ int) (time.Duration, error) {
			return offset, nil
		}, func() (bool, error) {
			return false, nil
		})
	timeClock.Apply(clock.Options{Servers: []string{"ntp.test"}, Tolerance: time.Millisecond,
		Wait: time.Second, Interval: time.Hour})
	defer timeClock.Close()
	manager, err := NewManager(Options{Resolve: sampleResolve,
		Directory:  filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools, Clock: timeClock})
	require.NoError(t, err)
	manager.SetActive(true, config.Config{})
	defer manager.Close()
	outbound := config.Outbound{ID: "Denied", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
		Attempts: 2, ReconnectTimeout: 1, OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN,
			Username: "denied", Password: "x", OTPKey: "GEZDGNBVGY3TQOJQ",
			OTPMode: config.OTPChallenge}, Recovery: &config.Recovery{Enabled: true, Delay: 1}}
	manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
	status := waitState(t, manager, "Denied", StateFailed)
	require.Equal(t, "authentication failed", status.Message)
	log := strings.Join(manager.Log("Denied"), "\n")
	require.Regexp(t, `waiting [1-5]s for a new one-time code`, log)
	require.Contains(t, log, "connection failed after 2 of 2 attempts")
	require.NotContains(t, log, "recovery attempt")
}

func TestManager_Services(t *testing.T) {
	tools, directory := makeTools(t)
	state := filepath.Join(directory, "state")
	port, _ := makeInboundPort(t)
	snapshot := config.Default()
	_, err := snapshot.AddInbound(config.Inbound{Type: config.TypeSocks5, ID: "Socks", Port: port,
		Enabled: true, Autostart: true, Anonymous: true}, time.Now())
	require.NoError(t, err)
	snapshot.Outbounds = []config.Outbound{{ID: "AWG", Type: config.TypeAmneziaWG, Enabled: true,
		Autostart: true, Slot: 4, ReconnectTimeout: 1,
		AmneziaWG:  &config.AmneziaWGOptions{Config: sampleTunnel},
		Monitoring: &config.Monitoring{Interval: 5, Timeout: 1, Retries: 1, Target: "127.0.0.1:1"}}}
	open := func() *Manager {
		manager, err := NewManager(Options{Resolve: sampleResolve,
			Directory: filepath.Join(directory, "run"),
			StateDir:  state, Tools: tools})
		require.NoError(t, err)
		t.Cleanup(manager.Close)
		return manager
	}
	apply := func(manager *Manager, active bool) {
		manager.SetActive(active, snapshot)
		manager.Apply(snapshot.Outbounds, snapshot.Settings)
		manager.ApplyInbounds(snapshot)
	}
	journal := func() string {
		data, err := os.ReadFile(filepath.Join(state, journalName))
		require.NoError(t, err)
		return string(data)
	}
	manager := open()
	apply(manager, false)
	require.Equal(t, StateStopped, manager.Status()["AWG"].State)
	require.Equal(t, messageInactive, manager.Status()["AWG"].Message)
	require.Equal(t, StateStopped, manager.Inbounds()["Socks"].State)
	require.Empty(t, manager.Serving())
	require.EqualError(t, manager.Control("AWG", ActionStart),
		"failed to start outbound 'AWG': router is stopped")
	require.EqualError(t, manager.ControlInbound("Socks", ActionRestart),
		"failed to restart inbound 'Socks': router is stopped")
	apply(manager, true)
	waitInbound(t, manager, "Socks", StateUp)
	require.Equal(t, map[string]bool{"Socks": true}, manager.Serving())
	status := waitState(t, manager, "AWG", StateFailed)
	require.Contains(t, journal(), `"state": "failed"`)
	require.NoError(t, manager.ControlInbound("Socks", ActionStop))
	require.Equal(t, InboundStatus{State: StateStopped, Message: messageStopped,
		Since: manager.Inbounds()["Socks"].Since, Clients: []InboundClient{}},
		manager.Inbounds()["Socks"])
	require.Empty(t, manager.Serving())
	manager.Close()
	{
		manager := open()
		apply(manager, true)
		require.Equal(t, StateFailed, manager.Status()["AWG"].State)
		require.Equal(t, status.Message, manager.Status()["AWG"].Message)
		require.Equal(t, StateStopped, manager.Inbounds()["Socks"].State)
		log := strings.Join(manager.Log("AWG"), "\n")
		require.Contains(t, log, "boom")
		require.Contains(t, log, "stopped: Proximity is shutting down")
		require.Contains(t, strings.Join(manager.InboundLog("Socks"), "\n"), messageStopped)
		require.NoError(t, manager.ControlInbound("Socks", ActionStart))
		waitInbound(t, manager, "Socks", StateUp)
		require.NoError(t, manager.ControlInbound("Socks", ActionStart))
		require.NoError(t, manager.ControlInbound("Socks", ActionRestart))
		waitInbound(t, manager, "Socks", StateUp)
		require.Equal(t, 1, strings.Count(strings.Join(manager.InboundLog("Socks"), "\n"),
			"starting"))
		require.NotContains(t, strings.Join(manager.InboundLog("Socks"), "\n"), messageStopped)
		snapshot.Settings.ClearLogOnRestart = false
		apply(manager, true)
		require.NoError(t, manager.ControlInbound("Socks", ActionRestart))
		waitInbound(t, manager, "Socks", StateUp)
		require.Equal(t, 2, strings.Count(strings.Join(manager.InboundLog("Socks"), "\n"),
			"starting"))
		require.NoError(t, manager.Control("AWG", ActionStop))
		require.Equal(t, messageStopped, manager.Status()["AWG"].Message)
		require.NoError(t, manager.Control("AWG", ActionStart))
		waitState(t, manager, "AWG", StateFailed)
		require.Contains(t, strings.Join(manager.Log("AWG"), "\n"), messageStopped)
		snapshot.Settings.ClearLogOnRestart = true
		apply(manager, true)
		require.NoError(t, manager.Control("AWG", ActionRestart))
		waitState(t, manager, "AWG", StateFailed)
		require.NotContains(t, strings.Join(manager.Log("AWG"), "\n"), messageStopped)
		require.NotContains(t, strings.Join(manager.Log("AWG"), "\n"), "shutting down")
		snapshot.Inbounds[0].Autostart, snapshot.Outbounds[0].Autostart = false, false
		apply(manager, false)
		require.Equal(t, messageInactive, manager.Inbounds()["Socks"].Message)
		require.Contains(t, strings.Join(manager.InboundLog("Socks"), "\n"),
			"stopped: router is stopped")
		apply(manager, true)
		require.Equal(t, messageAutostart, manager.Inbounds()["Socks"].Message)
		require.Equal(t, StateFailed, manager.Status()["AWG"].State)
		snapshot.Outbounds[0].ReconnectTimeout = 2
		apply(manager, true)
		require.Eventually(t, func() bool {
			return strings.Count(strings.Join(manager.Log("AWG"), "\n"), "starting\n") >= 2
		}, 5*time.Second, 20*time.Millisecond)
	}
	{
		manager := open()
		snapshot.Inbounds[0].Outbound = "gone"
		apply(manager, true)
		require.EqualError(t, manager.ControlInbound("Socks", ActionStart),
			"failed to start inbound 'Socks': outbound 'gone' does not exist")
		require.EqualError(t, manager.ControlInbound("missing", ActionStart),
			"inbound 'missing' does not exist")
		snapshot.Inbounds[0].Enabled, snapshot.Outbounds[0].Enabled = false, false
		apply(manager, true)
		require.EqualError(t, manager.ControlInbound("Socks", ActionStop),
			"inbound 'Socks' is disabled")
		require.EqualError(t, manager.Control("AWG", ActionStop), "outbound 'AWG' is disabled")
		require.NotContains(t, journal(), outboundKey(4))
		require.NotContains(t, journal(), inboundKey(port))
		require.FileExists(t, filepath.Join(state, logsDirectory, outboundKey(4)+logSuffix))
		snapshot.Inbounds, snapshot.Outbounds = []config.Inbound{}, []config.Outbound{}
		apply(manager, true)
		require.NoFileExists(t, filepath.Join(state, logsDirectory, outboundKey(4)+logSuffix))
		require.NoFileExists(t, filepath.Join(state, logsDirectory, inboundKey(port)+logSuffix))
		require.Empty(t, manager.Log("AWG"))
		require.Empty(t, manager.InboundLog("Socks"))
	}
}

func TestManager_Time(t *testing.T) {
	makeClock := func(offset time.Duration, reachable bool) *clock.Clock {
		result := clock.NewWith(filepath.Join(t.TempDir(), "clock.json"),
			func(_ context.Context, _ string, _ int) (time.Duration, error) {
				if !reachable {
					return 0, errors.New("unreachable")
				}
				return offset, nil
			}, func() (bool, error) {
				return false, nil
			})
		result.Apply(clock.Options{Servers: []string{"ntp.test"}, Tolerance: time.Second,
			Wait: 300 * time.Millisecond, Interval: time.Hour})
		t.Cleanup(result.Close)
		return result
	}
	outbound := config.Outbound{ID: "VPN", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
		Attempts: 3, ReconnectTimeout: 1, OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN,
			Username: "alice", Password: "wonderland", OTPKey: "GEZDGNBVGY3TQOJQ"}}
	start := func(timeClock *clock.Clock, faketime string) (*Manager, string) {
		tools, directory := makeTools(t)
		tools.Faketime = faketime
		manager, err := NewManager(Options{Resolve: sampleResolve,
			Directory:  filepath.Join(directory, "run"),
			Executable: "/usr/local/bin/proximity", Tools: tools, Clock: timeClock})
		require.NoError(t, err)
		manager.SetActive(true, config.Config{})
		t.Cleanup(manager.Close)
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		return manager, directory
	}
	{
		manager, _ := start(makeClock(0, false), "")
		status := waitState(t, manager, "VPN", StateFailed)
		require.Equal(t, "time is not synchronized after 300ms: NTP servers are unreachable "+
			"and host clock is not synchronized", status.Message)
		log := strings.Join(manager.Log("VPN"), "\n")
		require.Contains(t, log, "waiting for time synchronization, at most 300ms")
		require.NotContains(t, log, "attempt 2")
	}
	{
		manager, _ := start(makeClock(90*time.Second, true), "")
		status := waitState(t, manager, "VPN", StateFailed)
		require.Equal(t, "host clock is 1m30s off and libfaketime is not installed",
			status.Message)
	}
	{
		timeClock := makeClock(90*time.Second, true)
		manager, directory := start(timeClock, "/lib/libfaketime.so.1")
		waitState(t, manager, "VPN", StateUp)
		data, err := os.ReadFile(filepath.Join(directory, "environment.log"))
		require.NoError(t, err)
		require.Equal(t, "FAKETIME=+90.000\nLD_PRELOAD=/lib/libfaketime.so.1\n", string(data))
		require.Contains(t, strings.Join(manager.Log("VPN"), "\n"), "time: host clock is 1m30s "+
			"off 'ntp.test', Proximity uses its own time; OpenVPN runs with offset 1m30s")
		data, err = os.ReadFile(filepath.Join(directory, "run", "answers.txt"))
		require.NoError(t, err)
		code := strings.Split(strings.TrimSpace(string(data)), "|")[2]
		codes := []string{}
		for _, shift := range []time.Duration{-otpStep, 0, otpStep} {
			expected, err := computeOTP(outbound.OpenVPN.OTPKey, timeClock.Now().Add(shift))
			require.NoError(t, err)
			codes = append(codes, expected)
		}
		require.Contains(t, codes, code)
	}
	{
		manager, directory := start(makeClock(0, true), "/lib/libfaketime.so.1")
		waitState(t, manager, "VPN", StateUp)
		data, err := os.ReadFile(filepath.Join(directory, "environment.log"))
		require.NoError(t, err)
		require.Empty(t, string(data))
		require.Contains(t, strings.Join(manager.Log("VPN"), "\n"),
			"time: host clock is accurate: 0s from 'ntp.test'")
	}
	{
		tools, directory := makeTools(t)
		tools.OpenVPN = writeScript(t, directory, "openvpn-server", fakeServer)
		tools.Iptables = writeScript(t, directory, "iptables", fakeFirewall)
		tools.Forwarding = writeScript(t, directory, "ip_forward", "0\n")
		port, _ := makeInboundPort(t)
		manager, err := NewManager(Options{Resolve: sampleResolve,
			Directory:  filepath.Join(directory, "run"),
			Executable: "/usr/local/bin/proximity", Tools: tools, HostNetwork: true,
			Clock: makeClock(0, false)})
		require.NoError(t, err)
		manager.SetActive(true, config.Config{})
		t.Cleanup(manager.Close)
		snapshot := config.Default()
		_, err = snapshot.AddInbound(config.Inbound{Type: config.TypeOpenVPN, ID: "Server",
			Port: port, Enabled: true, Attempts: 3, Anonymous: true}, time.Now())
		require.NoError(t, err)
		manager.ApplyInbounds(snapshot)
		status := waitInbound(t, manager, "Server", StateReconnecting)
		require.Contains(t, status.Message,
			"recovery attempt 1: time is not synchronized after 300ms")
		require.NotContains(t, strings.Join(manager.InboundLog("Server"), "\n"), "attempt 2")
		snapshot.Inbounds[0].Recovery = &config.Recovery{Delay: 60}
		manager.ApplyInbounds(snapshot)
		status = waitInbound(t, manager, "Server", StateFailed)
		require.True(t, strings.HasPrefix(status.Message, "time is not synchronized after 300ms"))
	}
}

func TestMaskBits_Values(t *testing.T) {
	bits, err := maskBits("255.255.255.0")
	require.NoError(t, err)
	require.Equal(t, 24, bits)
	bits, err = maskBits("0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, 0, bits)
	{
		_, err := maskBits("bad")
		require.ErrorContains(t, err, `cannot parse "bad" as netmask`)
		_, err = maskBits("255.0.255.0")
		require.EqualError(t, err, "invalid netmask '255.0.255.0'")
	}
}

func TestNewManager_Problems(t *testing.T) {
	tools, directory := makeTools(t)
	blocker := writeScript(t, directory, "file", "x")
	{
		_, err := NewManager(Options{Resolve: sampleResolve,
			Directory: filepath.Join(blocker, "run"), Tools: tools})
		require.ErrorContains(t, err, "failed to create directory")
	}
	{
		tools.IP = filepath.Join(directory, "missing")
		_, err := NewManager(Options{Resolve: sampleResolve,
			Directory: filepath.Join(directory, "run"), Tools: tools})
		require.ErrorContains(t, err, "ip tool does not exist")
	}
}

func TestOpenJournal_Problems(t *testing.T) {
	directory := t.TempDir()
	blocker := writeScript(t, directory, "file", "x")
	{
		_, err := openJournal(filepath.Join(blocker, "state"))
		require.ErrorContains(t, err, "failed to create directory")
	}
	{
		state := filepath.Join(directory, "state")
		require.NoError(t, os.MkdirAll(filepath.Join(state, journalName), 0o700))
		_, err := openJournal(state)
		require.ErrorContains(t, err, "failed to read file")
		_, err = NewManager(Options{Resolve: sampleResolve,
			Directory: filepath.Join(directory, "run"), StateDir: state,
			Tools: Tools{IP: writeScript(t, directory, "ip", fakeIP)}})
		require.ErrorContains(t, err, "failed to read file")
	}
	{
		state := filepath.Join(directory, "broken")
		require.NoError(t, os.MkdirAll(state, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(state, journalName), []byte("{"), 0o600))
		journal, err := openJournal(state)
		require.NoError(t, err)
		require.Empty(t, journal.records)
		journal.set("outbound-1", ServiceRecord{State: StateStopped})
		journal.set("outbound-1", ServiceRecord{State: StateStopped})
		reopened, err := openJournal(state)
		require.NoError(t, err)
		require.Equal(t, map[string]ServiceRecord{"outbound-1": {State: StateStopped}},
			reopened.records)
	}
}

func TestPortListening_Tables(t *testing.T) {
	address := makeListener(t)
	_, text, err := net.SplitHostPort(address)
	require.NoError(t, err)
	port, err := strconv.Atoi(text)
	require.NoError(t, err)
	listening, known := portListening(port)
	require.True(t, known)
	require.True(t, listening)
	acceptor := socksAcceptor{port: port, target: address}
	require.NoError(t, acceptor.start(context.Background()))
	require.Nil(t, acceptor.exited())
	{
		tables := socketTables
		defer func() {
			socketTables = tables
		}()
		socketTables = []string{filepath.Join(t.TempDir(), "missing")}
		listening, known := portListening(port)
		require.False(t, known)
		require.False(t, listening)
		require.NoError(t, acceptor.check(context.Background()))
		acceptor.target = "127.0.0.1:1"
		require.ErrorContains(t, acceptor.check(context.Background()),
			"listener does not accept connections")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.Error(t, acceptor.start(ctx))
	}
}

func TestResolveOpenVPN_Lines(t *testing.T) {
	answers := map[string][]string{"vpn.example.com": {"203.0.113.1", "203.0.113.2"},
		"gate.example.com": {"203.0.113.3", "203.0.113.4"}, "six.example.com": {"2001:db8::1"}}
	lookup := func(host string) ([]string, error) {
		if answers[host] == nil {
			return nil, fmt.Errorf("failed to resolve '%v': no such name", host)
		}
		return answers[host], nil
	}
	{
		text, notes, err := resolveOpenVPN("client\n  remote vpn.example.com 1194 udp\n"+
			"--remote 198.51.100.7 443 tcp\nremote missing.example.com 1195\n"+
			"http-proxy gate.example.com 3128 auto\nsocks-proxy 10.0.0.1 1080\n"+
			"<connection>\nremote vpn.example.com 1196\n</connection>\n"+
			"<ca>\nremote vpn.example.com 1\n</ca>\n# remote vpn.example.com 2\nremote\n"+
			"remote six.example.com 1197\nverb 3\n", lookup)
		require.NoError(t, err)
		require.Equal(t, "client\nremote 203.0.113.1 1194 udp\nremote 203.0.113.2 1194 udp\n"+
			"--remote 198.51.100.7 443 tcp\nhttp-proxy 203.0.113.3 3128 auto\n"+
			"socks-proxy 10.0.0.1 1080\n<connection>\nremote 203.0.113.1 1196\n</connection>\n"+
			"<ca>\nremote vpn.example.com 1\n</ca>\n# remote vpn.example.com 2\nremote\n"+
			"remote 2001:db8::1 1197\nverb 3\n", text)
		require.Equal(t, []string{"'vpn.example.com' is resolved to 203.0.113.1, 203.0.113.2",
			"remote 'missing.example.com' is skipped: failed to resolve 'missing.example.com': " +
				"no such name", "'gate.example.com' is resolved to 203.0.113.3",
			"'vpn.example.com' is resolved to 203.0.113.1",
			"'six.example.com' is resolved to 2001:db8::1"}, notes)
	}
	{
		text, notes, err := resolveOpenVPN("client\nremote 198.51.100.7 443\n", lookup)
		require.NoError(t, err)
		require.Equal(t, "client\nremote 198.51.100.7 443\n", text)
		require.Empty(t, notes)
		_, notes, err = resolveOpenVPN("remote a.example.com 1\nremote b.example.com 2\n", lookup)
		require.ErrorContains(t, err, "failed to resolve 'a.example.com': no such name")
		require.ErrorContains(t, err, "failed to resolve 'b.example.com': no such name")
		require.Len(t, notes, 2)
		_, _, err = resolveOpenVPN("remote 198.51.100.7 1\nsocks-proxy missing.example.com 1\n",
			lookup)
		require.EqualError(t, err, "failed to resolve 'missing.example.com': no such name")
		_, _, err = resolveOpenVPN("<connection>\nremote missing.example.com 1\n</connection>\n",
			lookup)
		require.EqualError(t, err, "failed to resolve 'missing.example.com': no such name")
	}
}

func TestRunVerify_Credentials(t *testing.T) {
	directory := t.TempDir()
	path, credentials := filepath.Join(directory, "auth.json"), filepath.Join(directory, "up.txt")
	now := time.Unix(1700000000, 0)
	verify := func(state authState, text string) error {
		data, err := json.Marshal(state)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		require.NoError(t, os.WriteFile(credentials, []byte(text), 0o600))
		return RunVerify(path, credentials, "203.0.113.5", "", now)
	}
	users := []authUser{{Name: "bob", Password: "builder"}}
	require.NoError(t, verify(authState{Users: users}, "bob\r\nbuilder\r\n"))
	require.NoError(t, verify(authState{Users: users}, "bob\nbuilder"))
	require.EqualError(t, verify(authState{Users: users}, "bob\nwrong\n"),
		"invalid password of user 'bob'")
	require.EqualError(t, verify(authState{Users: users}, "eve\nbuilder\n"),
		"unexpected user 'eve'")
	require.EqualError(t, verify(authState{Users: users}, "\n\n"),
		"anonymous connections are not allowed")
	require.EqualError(t, verify(authState{Users: users, Anonymous: true}, "\nsecret\n"),
		"anonymous connections are not allowed")
	require.NoError(t, verify(authState{Users: users, Anonymous: true}, ""))
	require.EqualError(t, verify(authState{Users: users, Anonymous: true}, "bob\nwrong\n"),
		"invalid password of user 'bob'")
	{
		key := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
		state := authState{Users: users, OTPKey: key}
		code, err := computeOTP(key, now)
		require.NoError(t, err)
		previous, err := computeOTP(key, now.Add(-otpStep))
		require.NoError(t, err)
		stale, err := computeOTP(key, now.Add(-3*otpStep))
		require.NoError(t, err)
		encode := base64.StdEncoding.EncodeToString
		require.NoError(t, verify(state, "bob\nbuilder"+code+"\n"))
		require.NoError(t, verify(state, "bob\nbuilder"+previous+"\n"))
		require.NoError(t, verify(state, "bob\nSCRV1:"+encode([]byte("builder"))+":"+
			encode([]byte(code))+"\n"))
		require.EqualError(t, verify(state, "bob\nbuilder"+stale+"\n"),
			"invalid one-time code of user 'bob'")
		require.EqualError(t, verify(state, "bob\nbuilder\n"), "invalid password of user 'bob'")
		require.EqualError(t, verify(state, "bob\nSCRV1:"+encode([]byte("wrong"))+":"+
			encode([]byte(code))+"\n"), "invalid password of user 'bob'")
		require.EqualError(t, verify(state, "bob\nSCRV1:!!:"+encode([]byte(code))+"\n"),
			"invalid password of user 'bob'")
		require.EqualError(t, verify(state, "bob\nabc\n"), "invalid password of user 'bob'")
		state.OTPKey = "1"
		require.EqualError(t, verify(state, "bob\nbuilder"+code+"\n"),
			"invalid one-time code of user 'bob'")
	}
	{
		key := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
		state := authState{Users: append(users, authUser{Name: "eve", Password: "listener"}),
			OTPKey: key}
		next, err := computeOTP(key, now.Add(otpStep))
		require.NoError(t, err)
		require.NoError(t, verify(state, "bob\nbuilder"+next+"\n"))
		require.NoError(t, verify(state, "bob\nbuilder"+next+"\n"))
		require.EqualError(t, verify(state, "eve\nlistener"+next+"\n"),
			"one-time code of user 'eve' is already used")
		require.EqualError(t, RunVerify(path, credentials, "203.0.113.9", "", now),
			"one-time code of user 'eve' is already used")
		require.NoError(t, os.WriteFile(credentials, []byte("bob\nbuilder"+next+"\n"), 0o600))
		require.EqualError(t, RunVerify(path, credentials, "203.0.113.9", "", now),
			"one-time code of user 'bob' is already used")
		t.Setenv(addressVariable, "198.51.100.7")
		require.Equal(t, "198.51.100.7", VerifyAddress())
	}
	{
		state := authState{Users: users, OTPKey: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}
		require.EqualError(t, verify(state, "bob\nSESS_ID_AT_token\n"),
			"invalid password of user 'bob'")
		data, err := json.Marshal(state)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		token := func(name string, session string) error {
			require.NoError(t, os.WriteFile(credentials, []byte(name+"\nSESS_ID_AT_token\n"), 0o600))
			return RunVerify(path, credentials, "203.0.113.5", session, now)
		}
		require.NoError(t, token("bob", "Authenticated"))
		require.EqualError(t, token("eve", "Authenticated"), "unexpected user 'eve'")
		require.EqualError(t, token("", "AuthenticatedEmptyUser"),
			"anonymous connections are not allowed")
		for _, session := range []string{"Initial", "Expired", "Invalid", "ExpiredEmptyUser"} {
			require.EqualError(t, token("bob", session), "invalid password of user 'bob'")
		}
		state.Anonymous = true
		data, err = json.Marshal(state)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		require.NoError(t, token("", "AuthenticatedEmptyUser"))
		t.Setenv(sessionVariable, "Authenticated")
		require.Equal(t, "Authenticated", VerifySession())
	}
	{
		key := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
		offsetPath := filepath.Join(directory, "clock.json")
		require.NoError(t, os.WriteFile(offsetPath, []byte(`{"offset":600000}`), 0o600))
		state := authState{Users: users, OTPKey: key, Clock: offsetPath}
		corrected, err := computeOTP(key, now.Add(10*time.Minute))
		require.NoError(t, err)
		host, err := computeOTP(key, now)
		require.NoError(t, err)
		require.NoError(t, verify(state, "bob\nbuilder"+corrected+"\n"))
		require.EqualError(t, verify(state, "bob\nbuilder"+host+"\n"),
			"invalid one-time code of user 'bob'")
		state.Clock = filepath.Join(directory, "missing.json")
		require.NoError(t, verify(state, "bob\nbuilder"+host+"\n"))
	}
	{
		require.ErrorContains(t, RunVerify(filepath.Join(directory, "missing"), credentials, "",
			"", now), "failed to read file")
		require.ErrorContains(t, RunVerify(path, filepath.Join(directory, "missing"), "", "", now),
			"failed to read file")
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
		require.ErrorContains(t, RunVerify(path, credentials, "", "", now),
			"failed to decode file")
	}
}

func TestRunHook_Environment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.json")
	require.NoError(t, RunHook(path, []string{"route_vpn_gateway=10.8.0.1", "dev=tun0",
		"ifconfig_local=10.8.0.2", "foreign_option_1=dhcp-option DNS 10.8.0.1", "PATH=/bin",
		"password=secret", "script_type=up", "broken"}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	values := map[string]string{}
	require.NoError(t, json.Unmarshal(data, &values))
	require.Equal(t, map[string]string{"route_vpn_gateway": "10.8.0.1", "dev": "tun0",
		"ifconfig_local": "10.8.0.2", "foreign_option_1": "dhcp-option DNS 10.8.0.1",
		"script_type": "up"}, values)
	{
		err := RunHook(filepath.Join(path, "nested"), nil)
		require.ErrorContains(t, err, "failed to write file")
	}
}

func TestSanitizeOpenVPN_Directives(t *testing.T) {
	source := "client\r\ndev tap0\n  daemon\nauth-user-pass /etc/secret.txt\n--up /bin/evil\n" +
		"script-security 3\nremote vpn.example.com 1194\n<ca>\ndev inside block\n</ca>\n" +
		"log /var/log/vpn.log\ndev-type tap"
	text, kind := sanitizeOpenVPN(source, false)
	require.Equal(t, "tap", kind)
	require.Equal(t, "client\nremote vpn.example.com 1194\n<ca>\ndev inside block\n</ca>\n"+
		"auth-user-pass\n", text)
	{
		text, kind := sanitizeOpenVPN("client\ndev tun\nremote a 1\n", true)
		require.Equal(t, "tun", kind)
		require.Equal(t, "client\nremote a 1\n\nauth-user-pass\n", text)
	}
	{
		text, kind := sanitizeOpenVPN("client\nremote a 1", false)
		require.Equal(t, "tun", kind)
		require.Equal(t, "client\nremote a 1\n", text)
	}
}

func TestSignature_Changes(t *testing.T) {
	settings := config.Settings{ProbeTarget: "1.1.1.1:443", CheckInterval: 30}
	outbound := config.Outbound{ID: "S", Type: config.TypeSocks5, Enabled: true, Slot: 1,
		Socks5: &config.Socks5Options{Server: "10.0.0.1", Port: 1080}}
	signature := makeSignature(outbound, settings, 0)
	renamed := outbound
	renamed.ID, renamed.Autostart, renamed.Statistics = "id", true, true
	require.Equal(t, signature, makeSignature(renamed, settings, 0))
	require.NotEqual(t, signature, makeSignature(outbound, settings, CaptureMark))
	changed := outbound.Clone()
	changed.Socks5.Port = 1081
	require.NotEqual(t, signature, makeSignature(changed, settings, 0))
	settings.CheckInterval = 60
	require.NotEqual(t, signature, makeSignature(outbound, settings, 0))
	settings.CheckInterval, settings.ConnectivityChecks = 30, true
	require.NotEqual(t, signature, makeSignature(outbound, settings, 0))
	{
		tunnel := config.Outbound{ID: "T", Type: config.TypeOpenVPN, Enabled: true, Slot: 2,
			OpenVPN:    &config.OpenVPNOptions{Config: sampleOpenVPN},
			Monitoring: &config.Monitoring{Enabled: true, Interval: 30}}
		signature := makeSignature(tunnel, settings, 0)
		settings.CheckInterval, settings.ConnectivityChecks = 60, false
		require.Equal(t, signature, makeSignature(tunnel, settings, 0))
		require.NotEqual(t, signature, makeSignature(tunnel, settings, CaptureMark))
		tunnel.Monitoring = &config.Monitoring{Interval: 30}
		signature = makeSignature(tunnel, settings, 0)
		settings.CheckInterval = 90
		require.NotEqual(t, signature, makeSignature(tunnel, settings, 0))
	}
	{
		inbound := config.Inbound{ID: "I", Type: config.TypeOpenVPN, Port: 7800,
			OpenVPN: &config.OpenVPNServer{MaxClients: 5}}
		signature := inboundSignature(inbound, 0)
		renamed := inbound.Clone()
		renamed.ID, renamed.Autostart, renamed.Statistics = "id", true, true
		renamed.Outbound, renamed.Anonymous = config.ViaHost, true
		renamed.Users = []config.InboundUser{{Name: "a"}}
		require.Equal(t, signature, inboundSignature(renamed, 0))
		require.NotEqual(t, signature, inboundSignature(inbound, CaptureMark))
		renamed.OpenVPN.MaxClients = 6
		require.NotEqual(t, signature, inboundSignature(renamed, 0))
		inbound.Port = 7801
		require.NotEqual(t, signature, inboundSignature(inbound, 0))
	}
	{
		first := Status{State: StateUp, DNS: []string{"1.1.1.1"}}
		second := Status{State: StateUp, DNS: []string{"1.1.1.1"}, Latency: 5}
		require.True(t, sameStatus(first, second))
		second.Routes = []string{"10.0.0.0/8"}
		require.False(t, sameStatus(first, second))
		second = first
		second.State = StateDown
		require.False(t, sameStatus(first, second))
	}
	{
		failure := &fatalError{cause: os.ErrNotExist}
		require.Equal(t, "file does not exist", failure.Error())
		require.ErrorIs(t, failure, os.ErrNotExist)
	}
}

func TestUsesTCP_Directives(t *testing.T) {
	require.False(t, usesTCP(""))
	require.False(t, usesTCP(sampleOpenVPN))
	require.False(t, usesTCP("client\nproto udp\nremote vpn.example.com 1194\nproto\n"))
	require.False(t, usesTCP("client\n# remote a 1 tcp\n;proto tcp\nremote a 1\n"))
	require.True(t, usesTCP("client\nproto tcp-client\nremote vpn.example.com 443\n"))
	require.True(t, usesTCP("client\n  --proto tcp4\n"))
	require.True(t, usesTCP("client\nremote vpn.example.com 1194 udp\nremote b 443 tcp\n"))
	require.True(t, usesTCP("<connection>\nremote vpn.example.com 443 tcp-client\n</connection>\n"))
}
