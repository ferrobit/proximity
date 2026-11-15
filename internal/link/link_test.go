package link

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
	"proximity/internal/wgconf"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"DNS = 10.8.1.1\n[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = 127.0.0.1:51820\n" +
	"AllowedIPs = 0.0.0.0/0, 10.55.0.9, 10.66.0.0/16\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\ndaemon\n"
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
hook=""
while [ $# -gt 0 ]; do
	if [ "$1" = "--up" ]; then hook="${2##* }"; fi
	shift
done
exec 3<>/dev/tty
printf 'Enter Auth Username:' >&3
read -r user <&3
printf 'Enter Auth Password:' >&3
read -r pass <&3
if [ "$user" = "denied" ]; then
	echo "2026-01-01 00:00:00 AUTH: Received control message: AUTH_FAILED"
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

func TestManager_AmneziaFailure(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Directory: filepath.Join(directory, "run"), Tools: tools})
	require.NoError(t, err)
	defer manager.Close()
	outbound := config.Outbound{ID: "AWG", Type: config.TypeAmneziaWG, Enabled: true, Slot: 4,
		AmneziaWG: &config.AmneziaWGOptions{Config: sampleTunnel},
		Monitoring: &config.Monitoring{Interval: 5, Timeout: 1, Retries: 1, ReconnectDelay: 1,
			ReconnectAttempts: 1, Target: "127.0.0.1:1"}}
	manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
	require.False(t, manager.Capture(true, false))
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

func TestManager_OpenVPN(t *testing.T) {
	tools, directory := makeTools(t)
	changes := atomic.Int32{}
	manager, err := NewManager(Options{Directory: filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools, Changed: func() {
			changes.Add(1)
		}})
	require.NoError(t, err)
	defer manager.Close()
	outbound := config.Outbound{ID: "VPN", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
		OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "alice",
			Password: "wonderland", OTPKey: "GEZDGNBVGY3TQOJQ", OTPMode: config.OTPAppend},
		Monitoring: &config.Monitoring{Enabled: true, Interval: 5, Timeout: 1, Retries: 1,
			ReconnectDelay: 1, ReconnectAttempts: 0, Target: "127.0.0.1:1"}}
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
		require.Equal(t, "client\nremote vpn.example.com 1194 udp\n\nauth-user-pass\n", string(text))
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
		require.NotContains(t, string(arguments), "--mark")
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
}

func TestManager_Capture(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Directory: filepath.Join(directory, "run"),
		Executable: "/usr/local/bin/proximity", Tools: tools, HostNetwork: true})
	require.NoError(t, err)
	defer manager.Close()
	require.False(t, manager.Capture(true, true))
	calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
	require.NoError(t, err)
	for _, line := range []string{"-4 rule add priority 5150 fwmark 51000 lookup main",
		"-4 rule add priority 5155 iif lo ipproto 17 dport 53 lookup 51000",
		"-4 rule add priority 5155 iif lo ipproto 6 dport 53 lookup 51000",
		"-4 rule add priority 5160 lookup main suppress_prefixlength 0",
		"-4 rule add priority 5170 from 0.0.0.0/32 iif lo lookup 51000",
		"-4 rule add priority 5170 from 198.18.0.0/30 iif lo lookup 51000",
		"-6 rule add priority 5155 iif lo ipproto 17 dport 53 lookup 51000",
		"-6 rule add priority 5155 iif lo ipproto 6 dport 53 lookup 51000",
		"-6 rule add priority 5170 from ::/128 iif lo lookup 51000",
		"-6 rule add priority 5170 from fdfe:dcba:9876::/126 iif lo lookup 51000"} {
		require.Contains(t, string(calls), line+"\n")
	}
	require.NotContains(t, string(calls), "route replace default dev")
	{
		manager.capture.device = "lo"
		require.True(t, manager.Capture(true, true))
		require.True(t, manager.Capture(true, true))
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(string(calls),
			"-4 route replace default dev lo table 51000\n"))
		require.Equal(t, 1, strings.Count(string(calls),
			"-6 route replace default dev lo table 51000\n"))
		require.True(t, manager.Capture(true, false))
		calls, err = os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Equal(t, 2, strings.Count(string(calls),
			"-4 route replace default dev lo table 51000\n"))
	}
	{
		outbound := config.Outbound{ID: "VPN", Type: config.TypeOpenVPN, Enabled: true, Slot: 7,
			OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN, Username: "alice",
				Password: "wonderland", OTPKey: "GEZDGNBVGY3TQOJQ", OTPMode: config.OTPChallenge},
			Monitoring: &config.Monitoring{Interval: 5, Timeout: 1, Retries: 1, ReconnectDelay: 1,
				Target: "127.0.0.1:1"}}
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5, Capture: true})
		waitState(t, manager, "VPN", StateUp)
		arguments, err := os.ReadFile(filepath.Join(directory, "openvpn.log"))
		require.NoError(t, err)
		require.Contains(t, string(arguments), "--mark 51000")
		since := manager.Status()["VPN"].Since
		outbound.ID, outbound.Name = "Office", "renamed"
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5, Capture: true})
		require.NotContains(t, manager.Status(), "VPN")
		require.Equal(t, StateUp, manager.Status()["Office"].State)
		require.Equal(t, since, manager.Status()["Office"].Since)
		require.NotEmpty(t, manager.Log("Office"))
		require.Empty(t, manager.Log("VPN"))
		require.EqualError(t, manager.Restart("VPN"), "outbound 'VPN' is not running")
		require.NoError(t, manager.Restart("Office"))
		waitState(t, manager, "Office", StateUp)
		manager.Apply([]config.Outbound{outbound}, config.Settings{CheckInterval: 5})
		waitState(t, manager, "Office", StateUp)
		arguments, err = os.ReadFile(filepath.Join(directory, "openvpn.log"))
		require.NoError(t, err)
		require.NotContains(t, string(arguments), "--mark")
	}
	{
		require.False(t, manager.Capture(false, false))
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		require.NoError(t, err)
		require.Contains(t, string(calls), "-4 rule del priority 5155\n")
		require.Contains(t, string(calls), "-4 rule del priority 5170\n")
		require.Contains(t, string(calls), "-6 route flush table 51000\n")
	}
	{
		plain, err := NewManager(Options{Directory: filepath.Join(directory, "plain"), Tools: tools})
		require.NoError(t, err)
		defer plain.Close()
		plain.capture.device = "lo"
		require.False(t, plain.Capture(true, false))
	}
}

func TestManager_HostAttempts(t *testing.T) {
	tools, directory := makeTools(t)
	manager, err := NewManager(Options{Directory: filepath.Join(directory, "run"), Tools: tools})
	require.NoError(t, err)
	defer manager.Close()
	outbound := config.Outbound{ID: "M", Type: config.TypeHost, Enabled: true, Slot: 5,
		Host: &config.HostOptions{Mode: config.HostInterface, Interface: "missing0", Attempts: 2,
			ReconnectTimeout: 1}}
	settings := config.Settings{ProbeTarget: "127.0.0.1:1", CheckInterval: 5,
		ConnectivityChecks: true}
	manager.Apply([]config.Outbound{outbound}, settings)
	status := waitState(t, manager, "M", StateReconnecting)
	require.Equal(t, "reconnect attempt 1 of 1: interface 'missing0' does not exist",
		status.Message)
	status = waitState(t, manager, "M", StateFailed)
	require.Equal(t, "interface 'missing0' does not exist", status.Message)
	{
		outbound.Host = &config.HostOptions{Mode: config.HostDefault, Attempts: 1,
			ReconnectTimeout: 1}
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
	manager, err := NewManager(Options{Directory: filepath.Join(directory, "run"), Tools: tools})
	require.NoError(t, err)
	settings := config.Settings{ProbeTarget: target, CheckInterval: 5, ConnectivityChecks: true}
	outbounds := []config.Outbound{
		{ID: "S", Type: config.TypeSocks5, Enabled: true, Slot: 1,
			Socks5: &config.Socks5Options{Server: host, Port: number}},
		{ID: "H", Type: config.TypeHTTP, Enabled: true, Slot: 2,
			HTTP: &config.HTTPOptions{Server: "127.0.0.1", Port: 1}},
		{ID: "D", Type: config.TypeHost, Enabled: true, Slot: 3,
			Host: &config.HostOptions{Mode: config.HostDefault, Attempts: 3, ReconnectTimeout: 1}},
		{ID: "L", Type: config.TypeHost, Enabled: true, Slot: 6,
			Host: &config.HostOptions{Mode: config.HostInterface, Interface: "lo",
				Gateway: "127.0.0.1", Attempts: 3, ReconnectTimeout: 1}},
		{ID: "X", Type: config.TypeHost, Slot: 4, Host: &config.HostOptions{}}}
	manager.Apply(outbounds, settings)
	status := waitState(t, manager, "S", StateUp)
	require.Equal(t, target, status.Gateway)
	require.NotZero(t, status.Checked)
	require.GreaterOrEqual(t, status.Latency, 0)
	waitState(t, manager, "D", StateUp)
	status = waitState(t, manager, "H", StateDown)
	require.Contains(t, status.Message, "failed to connect to '127.0.0.1:1'")
	require.Eventually(t, func() bool {
		calls, err := os.ReadFile(filepath.Join(directory, "ip.log"))
		return err == nil && strings.Contains(string(calls),
			"rule add priority 5100 from 127.0.0.1/32 lookup 51006")
	}, 5*time.Second, 20*time.Millisecond)
	require.NotContains(t, manager.Status(), "X")
	require.Contains(t, strings.Join(manager.Log("H"), "\n"), "connectivity check 1 of 1 failed")
	require.Empty(t, manager.Log("missing"))
	require.Empty(t, manager.Counters())
	{
		require.NoError(t, manager.Restart("S"))
		waitState(t, manager, "S", StateUp)
		require.EqualError(t, manager.Restart("missing"), "outbound 'missing' is not running")
	}
	{
		outbounds[0].Enabled, outbounds[2].Name = false, "renamed"
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
		require.Empty(t, manager.Status())
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
		_, err := NewManager(Options{Directory: filepath.Join(blocker, "run"), Tools: tools})
		require.ErrorContains(t, err, "failed to create directory")
	}
	{
		tools.IP = filepath.Join(directory, "missing")
		_, err := NewManager(Options{Directory: filepath.Join(directory, "run"), Tools: tools})
		require.ErrorContains(t, err, "ip tool does not exist")
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
	renamed.ID, renamed.Name, renamed.Group, renamed.Statistics = "id", "name", "group", true
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
