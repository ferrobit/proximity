package link

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Tools struct {
	OpenVPN    string
	AmneziaGo  string
	AmneziaWG  string
	IP         string
	Ping       string
	Oathtool   string
	Iptables   string
	Ip6tables  string
	Forwarding string
	Faketime   string
}

type Interface struct {
	Name      string   `json:"name"`
	State     string   `json:"state"`
	Addresses []string `json:"addresses"`
}

const tableBase = 51000
const tableLimit = 61000
const inboundTableLimit = 61100
const rulePriority = "5100"
const inboundPriority = "5140"
const forwardingPath = "/proc/sys/net/ipv4/ip_forward"
const forwardChain = "FORWARD"
const firewallWait = "5"
const blackholeMetric = "4294967295"
const devicePrefix = "proximity_"

var tablePattern = regexp.MustCompile(`\btable (\d+)\b`)
var devicePattern = regexp.MustCompile(`^` + devicePrefix + `[aio]\d+$`)
var forwardPattern = regexp.MustCompile(`^-A ` + forwardChain + ` (-[io] ` + devicePrefix +
	`i\d+ -j ACCEPT)$`)
var hookKeys = []string{"dev", "script_type", "ifconfig_", "route_", "foreign_option_"}

func DefaultTools() Tools {
	return Tools{OpenVPN: lookup("PROXIMITY_OPENVPN", "openvpn"),
		AmneziaGo: lookup("PROXIMITY_AWG_GO", "amneziawg-go"),
		AmneziaWG: lookup("PROXIMITY_AWG", "awg"), IP: lookup("PROXIMITY_IP", "ip"),
		Ping: lookup("PROXIMITY_PING", "ping"), Oathtool: lookup("PROXIMITY_OATHTOOL", "oathtool"),
		Iptables: lookup("PROXIMITY_IPTABLES", "iptables"),
		Ip6tables: lookup("PROXIMITY_IP6TABLES",
			strings.Replace(lookup("PROXIMITY_IPTABLES", "iptables"), "iptables", "ip6tables", 1)),
		Forwarding: lookup("PROXIMITY_FORWARDING", forwardingPath),
		Faketime:   lookup("PROXIMITY_FAKETIME", findFaketime())}
}

func InboundTable(slot int) int {
	return tableLimit + slot
}

func Table(slot int) int {
	return tableBase + slot
}

func Interfaces() ([]Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}
	result := []Interface{}
	for _, item := range interfaces {
		if item.Flags&net.FlagLoopback != 0 || devicePattern.MatchString(item.Name) {
			continue
		}
		entry := Interface{Name: item.Name, State: linkState(item.Name), Addresses: []string{}}
		if addresses, err := item.Addrs(); err == nil {
			for _, address := range addresses {
				entry.Addresses = append(entry.Addresses, address.String())
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

func RunHook(path string, environment []string) error {
	values := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		for _, prefix := range hookKeys {
			if strings.HasPrefix(key, prefix) {
				values[key] = value
			}
		}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("failed to encode hook environment: %w", err)
	} else if err = os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return fmt.Errorf("failed to write file '%v': %w", path, err)
	} else if err = os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("failed to rename file '%v': %w", path, err)
	}
	return nil
}

func GenerateOTP(tools Tools, key string, now time.Time) (string, error) {
	if _, err := exec.LookPath(tools.Oathtool); err == nil {
		command := exec.Command(tools.Oathtool, "--totp", "--base32",
			"--now", now.UTC().Format("2006-01-02 15:04:05 UTC"), key)
		if output, err := command.Output(); err == nil {
			return strings.TrimSpace(string(output)), nil
		}
	}
	return computeOTP(key, now)
}

func lookup(variable string, fallback string) string {
	if value := os.Getenv(variable); value != "" {
		return value
	}
	return fallback
}

func computeOTP(key string, now time.Time) (string, error) {
	padding := strings.Repeat("=", (8-len(key)%8)%8)
	secret, err := base32.StdEncoding.DecodeString(strings.ToUpper(key) + padding)
	if err != nil {
		return "", fmt.Errorf("failed to decode OTP key: %w", err)
	}
	counter := make([]byte, 8)
	binary.BigEndian.PutUint64(counter, uint64(now.Unix()/30))
	digest := hmac.New(sha1.New, secret)
	digest.Write(counter)
	sum := digest.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", code%1000000), nil
}

func deviceAddresses(device string) []string {
	result := []string{}
	item, err := net.InterfaceByName(device)
	if err != nil {
		return result
	}
	addresses, err := item.Addrs()
	if err != nil {
		return result
	}
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil &&
			!prefix.Addr().IsLinkLocalUnicast() {
			result = append(result, prefix.Addr().String())
		}
	}
	return result
}

func linkState(device string) string {
	data, err := os.ReadFile("/sys/class/net/" + device + "/operstate")
	if err != nil {
		return "absent"
	} else if state := strings.TrimSpace(string(data)); state == "unknown" {
		return "up"
	} else {
		return state
	}
}

func dialProbe(ctx context.Context, target string, mark int, device string,
	timeout time.Duration) (time.Duration, error) {
	dialer := net.Dialer{Timeout: timeout, Control: func(_ string, _ string,
		connection syscall.RawConn) error {
		var failure error
		if err := connection.Control(func(descriptor uintptr) {
			if mark != 0 {
				failure = syscall.SetsockoptInt(int(descriptor), syscall.SOL_SOCKET,
					syscall.SO_MARK, mark)
			} else if device != "" {
				failure = syscall.BindToDevice(int(descriptor), device)
			}
		}); err != nil {
			return err
		}
		return failure
	}}
	started := time.Now()
	connection, err := dialer.DialContext(ctx, "tcp4", target)
	if err != nil {
		return 0, fmt.Errorf("failed to connect to '%v': %w", target, err)
	}
	elapsed := time.Since(started)
	if err = connection.Close(); err != nil {
		return 0, fmt.Errorf("failed to close connection to '%v': %w", target, err)
	}
	return elapsed, nil
}

func readCounter(device string, name string) (int64, error) {
	path := "/sys/class/net/" + device + "/statistics/" + name
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("failed to read file '%v': %w", path, err)
	}
	result, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q as counter: %w", data, err)
	}
	return result, nil
}

func maskBits(netmask string) (int, error) {
	address, err := netip.ParseAddr(netmask)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %q as netmask: %w", netmask, err)
	}
	ones, bits := net.IPMask(address.AsSlice()).Size()
	if bits == 0 {
		return 0, fmt.Errorf("invalid netmask '%v'", netmask)
	}
	return ones, nil
}

func (tools Tools) run(ctx context.Context, name string, arguments ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("failed to run '%v %v' (%v): %w", name,
			strings.Join(arguments, " "), text, err)
	}
	return text, nil
}

func (tools Tools) ip(arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tools.run(ctx, tools.IP, arguments...)
}

func (tools Tools) setupTable(table int) error {
	name := strconv.Itoa(table)
	tools.removeRules(name)
	if _, err := tools.ip("-4", "route", "replace", "blackhole", "default", "metric",
		blackholeMetric, "table", name); err != nil {
		return err
	} else if _, err = tools.ip("-4", "rule", "add", "priority", rulePriority, "fwmark", name,
		"lookup", name); err != nil {
		return err
	}
	if _, err := tools.ip("-6", "route", "replace", "blackhole", "default", "metric",
		blackholeMetric, "table", name); err == nil {
		_, _ = tools.ip("-6", "rule", "add", "priority", rulePriority, "fwmark", name, "lookup", name)
	}
	return nil
}

func (tools Tools) teardownTable(table int) {
	name := strconv.Itoa(table)
	tools.removeRules(name)
	_, _ = tools.ip("-4", "route", "flush", "table", name)
	_, _ = tools.ip("-6", "route", "flush", "table", name)
}

func (tools Tools) removeRules(name string) {
	for _, family := range []string{"-4", "-6"} {
		for _, priority := range []string{rulePriority, inboundPriority} {
			for range 16 {
				if _, err := tools.ip(family, "rule", "del", "priority", priority, "lookup",
					name); err != nil {
					break
				}
			}
		}
	}
}

func (tools Tools) setupInbound(table int, network string, device string) error {
	name := strconv.Itoa(table)
	tools.teardownTable(table)
	if _, err := tools.ip("-4", "route", "replace", "blackhole", "default", "metric",
		blackholeMetric, "table", name); err != nil {
		return err
	} else if _, err = tools.ip("-4", "route", "replace", network, "dev", device, "table",
		name); err != nil {
		return err
	} else if _, err = tools.ip("-4", "rule", "add", "priority", inboundPriority, "from", network,
		"lookup", name); err != nil {
		return err
	}
	return nil
}

func (tools Tools) enableForwarding() error {
	if data, err := os.ReadFile(tools.Forwarding); err == nil &&
		strings.TrimSpace(string(data)) == "1" {
		return nil
	} else if err = os.WriteFile(tools.Forwarding, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("failed to enable IP forwarding (sysctl net.ipv4.ip_forward=1): %w", err)
	}
	return nil
}

func (tools Tools) firewall(arguments ...string) (string, error) {
	return tools.firewallOf(tools.Iptables, arguments...)
}

func (tools Tools) firewallOf(binary string, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tools.run(ctx, binary, append([]string{"-w", firewallWait}, arguments...)...)
}

func (tools Tools) allowForward(device string) error {
	for _, direction := range []string{"-i", "-o"} {
		if _, err := tools.firewall("-C", forwardChain, direction, device, "-j",
			"ACCEPT"); err == nil {
			continue
		} else if _, err = tools.firewall("-I", forwardChain, "1", direction, device, "-j",
			"ACCEPT"); err != nil {
			return err
		}
	}
	return nil
}

func (tools Tools) filterPing(device string, blocked bool) error {
	rule := []string{"-i", device, "-p", "icmp", "-j", "REJECT", "--reject-with",
		"icmp-admin-prohibited"}
	for range 4 {
		if _, err := tools.firewall(append([]string{"-D", forwardChain}, rule...)...); err != nil {
			break
		}
	}
	if !blocked {
		return nil
	}
	// NOTE: The rule stands above the rules that accept the traffic of the device.
	_, err := tools.firewall(append([]string{"-I", forwardChain, "1"}, rule...)...)
	return err
}

func (tools Tools) denyForward(device string) {
	for _, direction := range []string{"-i", "-o"} {
		for range 4 {
			if _, err := tools.firewall("-D", forwardChain, direction, device, "-j",
				"ACCEPT"); err != nil {
				break
			}
		}
	}
}

func (tools Tools) sourceRules(table int, addresses []string) {
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address)
		if host, failure := netip.ParseAddr(address); err != nil && failure != nil {
			continue
		} else if err != nil {
			prefix = netip.PrefixFrom(host, host.BitLen())
		}
		family := "-4"
		if prefix.Addr().Is6() {
			family = "-6"
		}
		source := netip.PrefixFrom(prefix.Addr(), prefix.Addr().BitLen()).String()
		_, _ = tools.ip(family, "rule", "add", "priority", rulePriority, "from", source, "lookup",
			strconv.Itoa(table))
	}
}

func (tools Tools) removeDevice(device string) {
	if linkState(device) != "absent" {
		_, _ = tools.ip("link", "del", "dev", device)
	}
}

func (tools Tools) tableRoutes(table int, device string) []string {
	result := []string{}
	for _, family := range []string{"-4", "-6"} {
		output, err := tools.ip(family, "route", "show", "table", strconv.Itoa(table), "dev", device)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(output, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] == "default" {
				continue
			} else if prefix, err := netip.ParsePrefix(fields[0]); err == nil {
				result = append(result, prefix.Masked().String())
			} else if address, err := netip.ParseAddr(fields[0]); err == nil {
				result = append(result, netip.PrefixFrom(address, address.BitLen()).String())
			}
		}
	}
	return result
}

func (tools Tools) cleanup() error {
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %w", err)
	}
	for _, item := range interfaces {
		if devicePattern.MatchString(item.Name) {
			tools.removeDevice(item.Name)
		}
	}
	tables := map[string]bool{}
	for _, arguments := range [][]string{{"-4", "rule", "show"}, {"-6", "rule", "show"},
		{"-4", "route", "show", "table", "all"}, {"-6", "route", "show", "table", "all"}} {
		output, err := tools.ip(arguments...)
		if err != nil {
			continue
		}
		for _, match := range tablePattern.FindAllStringSubmatch(
			strings.ReplaceAll(output, "lookup", "table"), -1) {
			if table, err := strconv.Atoi(match[1]); err == nil && table > tableBase &&
				table < inboundTableLimit {
				tables[match[1]] = true
			}
		}
	}
	for name := range tables {
		if table, err := strconv.Atoi(name); err == nil {
			tools.teardownTable(table)
		}
	}
	capture, account := captureState{}, accountState{}
	capture.remove(tools)
	capture.removePing(tools)
	account.remove(tools)
	if output, err := tools.firewall("-S", forwardChain); err == nil {
		for _, line := range strings.Split(output, "\n") {
			if match := forwardPattern.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
				_, _ = tools.firewall(append([]string{"-D", forwardChain},
					strings.Fields(match[1])...)...)
			}
		}
	}
	if _, err = exec.LookPath(tools.IP); err != nil {
		return errors.New("ip tool does not exist")
	}
	return nil
}

func (tools Tools) loosenRoute(device string) {
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/"+device+"/rp_filter", []byte("2"), 0o644)
}
