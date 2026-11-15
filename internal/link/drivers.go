package link

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"proximity/internal/config"
	"proximity/internal/wgconf"
)

type driver interface {
	start(ctx context.Context) error
	check(ctx context.Context, timeout time.Duration) (time.Duration, error)
	exited() <-chan error
	describe(status *Status)
	stop()
	dispose()
}

type hostDriver struct {
	runner  *runner
	tools   Tools
	options config.HostOptions
	table   int
}

type proxyDriver struct {
	target string
	bypass int
}

type amneziaDriver struct {
	runner    *runner
	tools     Tools
	options   config.AmneziaWGOptions
	target    string
	directory string
	device    string
	table     int
	parsed    wgconf.Config
	command   *exec.Cmd
	finished  chan error
}

const amneziaTimeout = 10 * time.Second
const amneziaDefaultMTU = 1280
const amneziaSockets = "/var/run/amneziawg"

var amneziaPattern = regexp.MustCompile(`^(DEBUG: )?\(\S+\) \d{4}/\d\d/\d\d \d\d:\d\d:\d\d `)

func makeDriver(runner *runner) driver {
	endpoint, tools := runner.endpoint, runner.manager.options.Tools
	slot := strconv.Itoa(endpoint.Slot)
	switch endpoint.Type {
	case config.TypeHost:
		return &hostDriver{runner: runner, tools: tools, options: *endpoint.Host,
			table: Table(endpoint.Slot)}
	case config.TypeSocks5:
		return &proxyDriver{bypass: runner.bypass, target: net.JoinHostPort(
			endpoint.Socks5.Server, strconv.Itoa(endpoint.Socks5.Port))}
	case config.TypeHTTP:
		return &proxyDriver{bypass: runner.bypass, target: net.JoinHostPort(endpoint.HTTP.Server,
			strconv.Itoa(endpoint.HTTP.Port))}
	case config.TypeOpenVPN:
		return makeOpenVPNDriver(runner)
	default:
		return &amneziaDriver{runner: runner, tools: tools, options: *endpoint.AmneziaWG,
			target: endpoint.Monitoring.Target, table: Table(endpoint.Slot),
			directory: filepath.Join(runner.manager.options.Directory, "amneziawg-"+slot),
			device:    devicePrefix + "a" + slot}
	}
}

func (driver *hostDriver) marked() bool {
	return driver.options.Mode == config.HostInterface && driver.options.Gateway != ""
}

func (driver *hostDriver) start(_ context.Context) error {
	if driver.options.Mode != config.HostInterface {
		return nil
	} else if linkState(driver.options.Interface) == "absent" {
		return fmt.Errorf("interface '%v' does not exist", driver.options.Interface)
	} else if !driver.marked() {
		return nil
	} else if err := driver.tools.setupTable(driver.table); err != nil {
		return fmt.Errorf("failed to setup routing table (%v): %w", driver.table, err)
	} else if _, err = driver.tools.ip("route", "replace", "default", "via", driver.options.Gateway,
		"dev", driver.options.Interface, "table", strconv.Itoa(driver.table)); err != nil {
		return fmt.Errorf("failed to add default route to table (%v): %w", driver.table, err)
	}
	driver.tools.sourceRules(driver.table, deviceAddresses(driver.options.Interface))
	return nil
}

func (driver *hostDriver) check(ctx context.Context, timeout time.Duration) (time.Duration,
	error) {
	target := driver.runner.settings.ProbeTarget
	if driver.marked() {
		return dialProbe(ctx, target, driver.table, "", timeout)
	} else if driver.options.Mode == config.HostInterface {
		return dialProbe(ctx, target, driver.runner.bypass, driver.options.Interface, timeout)
	}
	return dialProbe(ctx, target, driver.runner.bypass, "", timeout)
}

func (driver *hostDriver) exited() <-chan error {
	return nil
}

func (driver *hostDriver) describe(status *Status) {
	if driver.options.Mode == config.HostInterface {
		status.Device, status.Gateway = driver.options.Interface, driver.options.Gateway
	}
}

func (driver *hostDriver) stop() {
}

func (driver *hostDriver) dispose() {
	if driver.marked() {
		driver.tools.teardownTable(driver.table)
	}
}

func (driver *proxyDriver) start(_ context.Context) error {
	return nil
}

func (driver *proxyDriver) check(ctx context.Context, timeout time.Duration) (time.Duration,
	error) {
	return dialProbe(ctx, driver.target, driver.bypass, "", timeout)
}

func (driver *proxyDriver) exited() <-chan error {
	return nil
}

func (driver *proxyDriver) describe(status *Status) {
	status.Gateway = driver.target
}

func (driver *proxyDriver) stop() {
}

func (driver *proxyDriver) dispose() {
}

func (driver *amneziaDriver) start(ctx context.Context) error {
	path := filepath.Join(driver.directory, "peer.conf")
	parsed, err := wgconf.Parse(driver.options.Config)
	if err != nil {
		return &fatalError{err}
	} else if err = os.MkdirAll(driver.directory, 0o700); err != nil {
		return fmt.Errorf("failed to create directory '%v': %w", driver.directory, err)
	} else if err = os.WriteFile(path, []byte(parsed.Strip()), 0o600); err != nil {
		return fmt.Errorf("failed to write file '%v': %w", path, err)
	} else if err = driver.tools.setupTable(driver.table); err != nil {
		return fmt.Errorf("failed to setup routing table (%v): %w", driver.table, err)
	}
	driver.parsed = parsed
	driver.tools.removeDevice(driver.device)
	driver.runner.logf("starting '%v' on device '%v'", driver.tools.AmneziaGo, driver.device)
	command := exec.Command(driver.tools.AmneziaGo, "--foreground", driver.device)
	command.Env = append(os.Environ(), "LOG_LEVEL=verbose", "WG_PROCESS_FOREGROUND=1")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	reader, writer := io.Pipe()
	command.Stdout, command.Stderr = writer, writer
	if err = command.Start(); err != nil {
		return fmt.Errorf("failed to start '%v': %w", driver.tools.AmneziaGo, err)
	}
	driver.command, driver.finished = command, make(chan error, 1)
	finished := driver.finished
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			driver.runner.logf("%v", amneziaPattern.ReplaceAllString(scanner.Text(), ""))
		}
	}()
	go func() {
		err := command.Wait()
		_ = writer.Close()
		if err != nil {
			finished <- fmt.Errorf("process '%v' exited: %w", command.Path, err)
		} else {
			finished <- errors.New("process exited")
		}
		close(finished)
	}()
	deadline := time.Now().Add(amneziaTimeout)
	socket := filepath.Join(amneziaSockets, driver.device+".sock")
	for !driver.ready(socket) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-finished:
			return err
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("device '%v' is not ready", driver.device)
		}
	}
	return driver.install(ctx, path)
}

func (driver *amneziaDriver) ready(socket string) bool {
	// NOTE: Until amneziawg-go listens on UAPI socket awg falls back to kernel module and fails.
	if linkState(driver.device) == "absent" {
		return false
	} else if connection, err := net.DialTimeout("unix", socket, time.Second); err != nil {
		return false
	} else {
		connection.Close()
		return true
	}
}

func (driver *amneziaDriver) install(ctx context.Context, path string) error {
	table, mtu := strconv.Itoa(driver.table), driver.parsed.MTU()
	if mtu == 0 {
		mtu = amneziaDefaultMTU
	}
	if _, err := driver.tools.run(ctx, driver.tools.AmneziaWG, "setconf", driver.device,
		path); err != nil {
		return &fatalError{err}
	}
	driver.runner.logf("configuration is applied to device '%v'", driver.device)
	if driver.runner.bypass != 0 {
		if _, err := driver.tools.run(ctx, driver.tools.AmneziaWG, "set", driver.device, "fwmark",
			strconv.Itoa(driver.runner.bypass)); err != nil {
			return fmt.Errorf("failed to set transport mark (%v): %w", driver.runner.bypass, err)
		}
	}
	families := map[string]bool{}
	for _, address := range driver.parsed.Addresses() {
		family := "-4"
		if strings.Contains(address, ":") {
			family = "-6"
		}
		if _, err := driver.tools.ip(family, "address", "replace", address, "dev",
			driver.device); err != nil && family == "-4" {
			return fmt.Errorf("failed to add address '%v': %w", address, err)
		} else if err == nil {
			families[family] = true
		}
	}
	if _, err := driver.tools.ip("link", "set", "dev", driver.device, "mtu", strconv.Itoa(mtu),
		"up"); err != nil {
		return fmt.Errorf("failed to enable device '%v': %w", driver.device, err)
	}
	driver.tools.loosenFilter(driver.device)
	driver.tools.sourceRules(driver.table, driver.parsed.Addresses())
	for family := range families {
		if _, err := driver.tools.ip(family, "route", "replace", "default", "dev", driver.device,
			"table", table); err != nil && family == "-4" {
			return fmt.Errorf("failed to add default route to table (%v): %w", driver.table, err)
		}
	}
	driver.runner.logf("device '%v' is up: address %v, MTU %v, routing table %v", driver.device,
		strings.Join(driver.parsed.Addresses(), ", "), mtu, table)
	if output, err := driver.tools.run(ctx, driver.tools.AmneziaWG, "show",
		driver.device); err == nil {
		for _, line := range strings.Split(output, "\n") {
			driver.runner.logf("%v", strings.TrimRight(line, " "))
		}
	}
	return nil
}

func (driver *amneziaDriver) check(ctx context.Context, timeout time.Duration) (time.Duration,
	error) {
	latency, err := dialProbe(ctx, driver.target, driver.table, "", timeout)
	if err == nil {
		driver.runner.logf("check '%v' through the tunnel: %v ms", driver.target,
			latency.Milliseconds())
	}
	return latency, err
}

func (driver *amneziaDriver) exited() <-chan error {
	return driver.finished
}

func (driver *amneziaDriver) describe(status *Status) {
	status.Device, status.DNS, status.Routes = driver.device, driver.parsed.DNS(), []string{}
	status.Address = strings.Join(driver.parsed.Addresses(), ", ")
	for _, allowed := range driver.parsed.AllowedIPs() {
		if prefix, err := netip.ParsePrefix(allowed); err == nil && prefix.Bits() != 0 {
			status.Routes = append(status.Routes, prefix.Masked().String())
		} else if address, err := netip.ParseAddr(allowed); err == nil {
			status.Routes = append(status.Routes,
				netip.PrefixFrom(address, address.BitLen()).String())
		}
	}
}

func (driver *amneziaDriver) stop() {
	if driver.command != nil && driver.command.Process != nil {
		_ = driver.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-driver.finished:
		case <-time.After(terminationTimeout):
			_ = driver.command.Process.Kill()
			<-driver.finished
		}
	}
	driver.command = nil
	driver.tools.removeDevice(driver.device)
}

func (driver *amneziaDriver) dispose() {
	driver.tools.teardownTable(driver.table)
	_ = os.RemoveAll(driver.directory)
}
