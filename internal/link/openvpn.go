package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"proximity/internal/config"
)

type openvpnDriver struct {
	runner    *runner
	tools     Tools
	options   config.OpenVPNOptions
	directory string
	device    string
	table     int
	terminal  *terminal
	mutex     sync.Mutex
	ready     chan struct{}
	failure   error
	attempts  int
	values    map[string]string
}

type deviceNetwork struct {
	device string
	prefix netip.Prefix
}

const openvpnTimeout = 60 * time.Second
const openvpnVerbosity = 3
const openvpnDebugVerbosity = 6
const openvpnReady = "Initialization Sequence Completed"
const openvpnAuthFailed = "AUTH_FAILED"
const openvpnChallenge = "AUTH_FAILED,CRV1"
const openvpnBadKey = "bad decrypt"
const openvpnRejected = "VERIFY ERROR: "
const inboundDevicePrefix = devicePrefix + "i"

var openvpnDropped = []string{"dev", "daemon", "log", "log-append", "status", "writepid",
	"management", "management-hold", "management-query-passwords", "management-client", "up",
	"down", "route-up", "route-pre-down", "ipchange", "script-security", "route-noexec",
	"auth-retry", "user", "group", "chroot", "askpass", "auth-user-pass", "cd", "up-restart",
	"down-pre", "iproute", "config", "plugin", "learn-address", "tls-verify", "client-connect"}
var usernamePrompt = regexp.MustCompile(`^Enter Auth Username:\s*$`)
var passwordPrompt = regexp.MustCompile(`^Enter Auth Password:\s*$`)
var keyPrompt = regexp.MustCompile(`^Enter Private Key Password:\s*$`)
var challengePrompt = regexp.MustCompile(`^CHALLENGE: `)
var stampPattern = regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d `)
var blockPattern = regexp.MustCompile(`^<(/?)([a-z0-9-]+)>$`)

func sanitizeOpenVPN(text string, login bool) (string, string) {
	result, kind, block, found := []string{}, "tun", "", false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if match := blockPattern.FindStringSubmatch(trimmed); match != nil && block == "" &&
			match[1] == "" {
			block = match[2]
		} else if match != nil && match[1] == "/" && match[2] == block {
			block = ""
		} else if fields := strings.Fields(trimmed); block == "" && len(fields) != 0 {
			name := strings.TrimPrefix(fields[0], "--")
			if name == "dev" && len(fields) > 1 && strings.HasPrefix(fields[1], "tap") {
				kind = "tap"
			} else if name == "dev-type" && len(fields) > 1 {
				kind = fields[1]
				continue
			}
			found = found || name == "auth-user-pass"
			dropped := false
			for _, directive := range openvpnDropped {
				dropped = dropped || directive == name
			}
			if dropped {
				continue
			}
		}
		result = append(result, line)
	}
	if found || login {
		result = append(result, "auth-user-pass")
	}
	return strings.Join(result, "\n") + "\n", kind
}

func usesTCP(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "--")
		if name == "proto" && strings.HasPrefix(fields[1], config.ProtocolTCP) {
			return true
		} else if name == "remote" && len(fields) > 3 && strings.HasPrefix(fields[3],
			config.ProtocolTCP) {
			return true
		}
	}
	return false
}

func inboundNetworks() []deviceNetwork {
	result := []deviceNetwork{}
	interfaces, err := net.Interfaces()
	if err != nil {
		return result
	}
	for _, item := range interfaces {
		addresses, err := item.Addrs()
		if err != nil || !strings.HasPrefix(item.Name, inboundDevicePrefix) {
			continue
		}
		for _, address := range addresses {
			if prefix, err := netip.ParsePrefix(address.String()); err == nil && prefix.Addr().Is4() {
				result = append(result, deviceNetwork{item.Name, prefix.Masked()})
			}
		}
	}
	return result
}

func findOverlap(values map[string]string, networks []deviceNetwork) error {
	for _, name := range []string{"ifconfig_local", "route_vpn_gateway"} {
		address, err := netip.ParseAddr(values[name])
		if err != nil {
			continue
		}
		for _, network := range networks {
			if network.prefix.Contains(address) {
				return fmt.Errorf("tunnel address '%v' overlaps network '%v' of inbound device '%v'",
					address, network.prefix, network.device)
			}
		}
	}
	return nil
}

func makeOpenVPNDriver(runner *runner) *openvpnDriver {
	slot := runner.outbound.Slot
	return &openvpnDriver{runner: runner, tools: runner.manager.options.Tools,
		options: *runner.outbound.OpenVPN, table: Table(slot),
		directory: filepath.Join(runner.manager.options.Directory, "openvpn-"+strconv.Itoa(slot)),
		device:    devicePrefix + "o" + strconv.Itoa(slot)}
}

func (driver *openvpnDriver) start(ctx context.Context) error {
	environment, err := driver.runner.manager.synchronize(ctx, driver.runner.logf)
	if err != nil {
		return err
	}
	hook := filepath.Join(driver.directory, "environment.json")
	path := filepath.Join(driver.directory, "client.ovpn")
	text, kind := sanitizeOpenVPN(driver.options.Config, driver.options.Username != "")
	if err = os.MkdirAll(driver.directory, 0o700); err != nil {
		return fmt.Errorf("failed to create directory '%v': %w", driver.directory, err)
	} else if err = os.WriteFile(path, []byte(text), 0o600); err != nil {
		return fmt.Errorf("failed to write file '%v': %w", path, err)
	} else if err = os.Remove(hook); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove file '%v': %w", hook, err)
	} else if err = driver.tools.setupTable(driver.table); err != nil {
		return fmt.Errorf("failed to setup routing table (%v): %w", driver.table, err)
	}
	driver.tools.removeDevice(driver.device)
	script := driver.runner.manager.options.Executable + " hook " + hook
	verbosity := openvpnVerbosity
	if driver.runner.outbound.Verbose {
		verbosity = openvpnDebugVerbosity
	}
	command := exec.Command(driver.tools.OpenVPN, "--config", path, "--cd", driver.directory,
		"--dev", driver.device, "--dev-type", kind, "--route-noexec", "--script-security", "2",
		"--up", script, "--route-up", script, "--auth-retry", "interact", "--auth-nocache",
		"--verb", strconv.Itoa(verbosity), "--ignore-unknown-option", "block-outside-dns",
		"register-dns")
	if usesTCP(text) {
		// NOTE: Without TCP_NODELAY every small packet waits for the delayed ACK of the peer.
		command.Args = append(command.Args, "--socket-flags", "TCP_NODELAY")
	}
	if driver.runner.bypass != 0 {
		command.Args = append(command.Args, "--mark", strconv.Itoa(driver.runner.bypass))
	}
	if len(environment) != 0 {
		command.Env = append(os.Environ(), environment...)
	}
	driver.runner.debugf("routing table %v, command: %v", driver.table,
		strings.Join(command.Args, " "))
	terminal, err := startTerminal(command)
	if err != nil {
		return err
	}
	driver.mutex.Lock()
	driver.terminal, driver.ready, driver.failure = terminal, make(chan struct{}), nil
	driver.attempts = 0
	ready := driver.ready
	driver.mutex.Unlock()
	go terminal.pump(driver.prompts(), driver.output)
	timer := time.NewTimer(openvpnTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("connection timed out")
	case err = <-terminal.exited:
		return driver.reason(err)
	case <-ready:
	}
	if data, err := os.ReadFile(hook); err != nil {
		return fmt.Errorf("failed to read file '%v': %w", hook, err)
	} else if err = json.Unmarshal(data, &driver.values); err != nil {
		return fmt.Errorf("failed to decode file '%v': %w", hook, err)
	} else if err = findOverlap(driver.values, inboundNetworks()); err != nil {
		// NOTE: Kernel drops tunnel packets when their addresses are local to an inbound.
		return &fatalError{err}
	}
	return driver.install(kind)
}

func (driver *openvpnDriver) install(kind string) error {
	table, gateway := strconv.Itoa(driver.table), driver.values["route_vpn_gateway"]
	driver.tools.loosenRoute(driver.device)
	driver.tools.sourceRules(driver.table, []string{driver.values["ifconfig_local"]})
	route := []string{"-4", "route", "replace", "default", "dev", driver.device, "table", table}
	if kind == "tap" && gateway != "" {
		route = append(route, "via", gateway)
	}
	if _, err := driver.tools.ip(route...); err != nil {
		return fmt.Errorf("failed to add default route to table (%v): %w", driver.table, err)
	}
	for index := 1; ; index++ {
		suffix := strconv.Itoa(index)
		network, found := driver.values["route_network_"+suffix]
		if !found {
			break
		}
		bits, err := maskBits(driver.values["route_netmask_"+suffix])
		if err != nil {
			driver.runner.logf("skipped route '%v': %v", network, err)
			continue
		}
		target := network + "/" + strconv.Itoa(bits)
		via := driver.values["route_gateway_"+suffix]
		if _, err = driver.tools.ip("-4", "route", "replace", target, "via", via, "dev",
			driver.device, "table", table); err == nil {
			continue
		} else if _, err = driver.tools.ip("-4", "route", "replace", target, "dev", driver.device,
			"table", table); err != nil {
			driver.runner.logf("skipped route '%v': %v", target, err)
		}
	}
	for index := 1; ; index++ {
		network, found := driver.values["route_ipv6_network_"+strconv.Itoa(index)]
		if !found {
			break
		} else if _, err := driver.tools.ip("-6", "route", "replace", network, "dev", driver.device,
			"table", table); err != nil {
			driver.runner.logf("skipped route '%v': %v", network, err)
		}
	}
	return nil
}

func (driver *openvpnDriver) prompts() []prompt {
	credential := func(value string, name string) func() (string, error) {
		return func() (string, error) {
			if driver.attempts++; value == "" {
				return "", &fatalError{fmt.Errorf("%v is required by server", name)}
			} else if driver.attempts > 12 {
				return "", &fatalError{errors.New("too many authentication prompts")}
			}
			return value, nil
		}
	}
	password := func() (string, error) {
		if driver.options.OTPKey == "" || driver.options.OTPMode != config.OTPAppend {
			return credential(driver.options.Password, "password")()
		}
		code, err := GenerateOTP(driver.tools, driver.options.OTPKey,
			driver.runner.manager.options.Clock.Now())
		if err != nil {
			return "", &fatalError{err}
		}
		return credential(driver.options.Password+code, "password")()
	}
	challenge := func() (string, error) {
		if driver.options.OTPKey == "" {
			return "", &fatalError{errors.New("OTP key is required by server")}
		}
		code, err := GenerateOTP(driver.tools, driver.options.OTPKey,
			driver.runner.manager.options.Clock.Now())
		if err != nil {
			return "", &fatalError{err}
		}
		return credential(code, "OTP code")()
	}
	return []prompt{{usernamePrompt, credential(driver.options.Username, "user name")},
		{passwordPrompt, password}, {challengePrompt, challenge},
		{keyPrompt, credential(driver.options.KeyPassword, "private key password")}}
}

func (driver *openvpnDriver) output(line string) {
	if line = strings.TrimSpace(stampPattern.ReplaceAllString(line, "")); line == "" {
		return
	}
	driver.runner.logf("%v", line)
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	if strings.Contains(line, openvpnReady) {
		if driver.attempts = 0; driver.ready != nil {
			close(driver.ready)
			driver.ready = nil
		}
	} else if strings.Contains(line, openvpnAuthFailed) && !strings.Contains(line,
		openvpnChallenge) && driver.failure == nil {
		if driver.failure = errors.New("authentication failed"); driver.options.OTPKey == "" {
			driver.failure = &fatalError{driver.failure}
		}
		driver.terminal.signal(syscall.SIGTERM)
	} else if strings.Contains(line, openvpnBadKey) && driver.failure == nil {
		driver.failure = &fatalError{errors.New("invalid private key password")}
		driver.terminal.signal(syscall.SIGTERM)
	} else if _, reason, found := strings.Cut(line, openvpnRejected); found &&
		driver.failure == nil {
		driver.failure = &fatalError{fmt.Errorf("server certificate is rejected: %v", reason)}
		driver.terminal.signal(syscall.SIGTERM)
	}
}

func (driver *openvpnDriver) reason(err error) error {
	driver.mutex.Lock()
	defer driver.mutex.Unlock()
	if driver.failure != nil {
		return driver.failure
	}
	return err
}

func (driver *openvpnDriver) check(ctx context.Context, timeout time.Duration) (time.Duration,
	error) {
	gateway := driver.values["route_vpn_gateway"]
	if gateway == "" {
		gateway = driver.values["ifconfig_remote"]
	}
	if _, err := netip.ParseAddr(gateway); err != nil {
		return 0, nil
	}
	seconds := strconv.Itoa(max(1, int(timeout.Seconds())))
	ctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()
	started := time.Now()
	if err := exec.CommandContext(ctx, driver.tools.Ping, "-n", "-c", "1", "-W", seconds, "-I",
		driver.device, gateway).Run(); err != nil {
		return 0, fmt.Errorf("failed to ping gateway '%v': %w", gateway, err)
	}
	return time.Since(started), nil
}

func (driver *openvpnDriver) exited() <-chan error {
	return driver.terminal.exited
}

func (driver *openvpnDriver) describe(status *Status) {
	status.Device, status.Gateway = driver.device, driver.values["route_vpn_gateway"]
	status.Address = driver.values["ifconfig_local"]
	status.Routes = driver.tools.tableRoutes(driver.table, driver.device)
	status.DNS, status.Domains = []string{}, []string{}
	for index := 1; ; index++ {
		option, found := driver.values["foreign_option_"+strconv.Itoa(index)]
		if !found {
			break
		} else if fields := strings.Fields(option); len(fields) != 3 || fields[0] != "dhcp-option" {
			continue
		} else if _, err := netip.ParseAddr(fields[2]); fields[1] == "DNS" && err == nil {
			status.DNS = append(status.DNS, fields[2])
		} else if strings.HasPrefix(fields[1], "DOMAIN") || fields[1] == "ADAPTER_DOMAIN_SUFFIX" {
			status.Domains = append(status.Domains, strings.ToLower(strings.Trim(fields[2], ".")))
		}
	}
}

func (driver *openvpnDriver) stop() {
	if driver.terminal != nil {
		driver.terminal.stop()
		driver.terminal = nil
	}
	driver.tools.removeDevice(driver.device)
	driver.values = nil
}

func (driver *openvpnDriver) dispose() {
	driver.tools.teardownTable(driver.table)
	_ = os.RemoveAll(driver.directory)
}
