package link

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"proximity/internal/clock"
	"proximity/internal/config"
)

type ExportOptions struct {
	User  string
	Embed bool
	Host  string
}

type authUser struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

type authState struct {
	Anonymous bool       `json:"anonymous"`
	OTPKey    string     `json:"otp_key"`
	Users     []authUser `json:"users"`
	Clock     string     `json:"clock,omitempty"`
}

type openvpnAcceptor struct {
	runner    *inboundRunner
	tools     Tools
	directory string
	device    string
	socket    string
	table     int
	network   netip.Prefix
	command   *exec.Cmd
	finished  chan error
	clients   []InboundClient
	index     int
	managed   bool
	mutex     sync.Mutex
	ready     chan struct{}
	reason    string
	state     authState
}

const InboundDNS = "198.18.0.2"
const serverTimeout = 60 * time.Second
const managementTimeout = 3 * time.Second
const otpStep = 30 * time.Second
const otpDigits = 6
const challengePrefix = "SCRV1:"
const challengeText = "Enter one-time code"
const clientListPrefix = "CLIENT_LIST,"
const clientListFields = 10
const undefinedUser = "UNDEF"
const anonymousUser = "anonymous"
const managementPrefix = "MANAGEMENT: "
const managementKill = managementPrefix + "CMD 'kill"
const optionsMarker = "Options error:"
const fatalMarker = "ERROR:"

var commonNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

func ClientConfig(inbound config.Inbound, options ExportOptions) (string, error) {
	server := inbound.OpenVPN
	if server == nil {
		return "", fmt.Errorf("unexpected inbound type '%v'", inbound.Type)
	}
	index := slices.IndexFunc(inbound.Users, func(user config.InboundUser) bool {
		return user.Name == options.User
	})
	host, port, protocol := server.Server, server.PublicPort, "udp"
	if host == "" {
		host = options.Host
	}
	if port == 0 {
		port = inbound.Port
	}
	if server.Protocol == config.ProtocolTCP {
		protocol = "tcp-client"
	}
	if options.User == "" && !inbound.Anonymous {
		return "", errors.New("anonymous connections are not allowed")
	} else if options.User != "" && index < 0 {
		return "", fmt.Errorf("user '%v' does not exist", options.User)
	} else if host == "" {
		return "", errors.New("server address does not exist")
	}
	lines := []string{"# Proximity inbound '" + inbound.ID + "'", "client", "dev tun",
		"proto " + protocol, "remote " + host + " " + strconv.Itoa(port), "resolv-retry infinite",
		"nobind", "persist-key", "persist-tun", "remote-cert-tls server"}
	if name := config.CommonName(server.Certificate); commonNamePattern.MatchString(name) {
		lines = append(lines, "verify-x509-name "+name+" name")
	}
	lines = append(lines, "auth "+server.Digest, "data-ciphers "+server.Ciphers,
		"tls-version-min "+server.TLSVersion, "tun-mtu "+strconv.Itoa(server.MTU))
	if server.Protocol == config.ProtocolUDP {
		lines = append(lines, "explicit-exit-notify 1")
	}
	if options.User != "" && !options.Embed {
		lines = append(lines, "auth-user-pass")
	}
	if options.User != "" && server.OTP {
		lines = append(lines, "auth-nocache", `static-challenge "`+challengeText+`" 1`)
	}
	if server.RedirectGateway {
		// NOTE: Only Windows clients know the pushed block-outside-dns option.
		lines = append(lines, "ignore-unknown-option block-outside-dns")
	}
	lines = append(lines, "verb 3", block("ca", server.CACertificate),
		block("cert", server.ClientCertificate), block("key", server.ClientKey),
		block("tls-crypt", server.TLSCrypt))
	if options.User != "" && options.Embed {
		lines = append(lines, block("auth-user-pass", options.User+"\n"+
			inbound.Users[index].Password+"\n"))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func RunVerify(path string, credentials string, now time.Time) error {
	state := authState{}
	if data, err := os.ReadFile(path); err != nil {
		return fmt.Errorf("failed to read file '%v': %w", path, err)
	} else if err = json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to decode file '%v': %w", path, err)
	}
	data, err := os.ReadFile(credentials)
	if err != nil {
		return fmt.Errorf("failed to read file '%v': %w", credentials, err)
	}
	name, rest, _ := strings.Cut(strings.ReplaceAll(string(data), "\r", ""), "\n")
	password, _, _ := strings.Cut(rest, "\n")
	if state.Clock != "" {
		// NOTE: Without the clock file the host clock is used.
		if offset, err := clock.ReadOffset(state.Clock); err == nil {
			now = now.Add(offset)
		}
	}
	return state.verify(name, password, now)
}

func block(name string, text string) string {
	return "<" + name + ">\n" + strings.TrimRight(text, "\n") + "\n</" + name + ">"
}

func makeAuthState(inbound config.Inbound) authState {
	result := authState{Anonymous: inbound.Anonymous, Users: []authUser{}}
	if inbound.OpenVPN.OTP {
		result.OTPKey = inbound.OpenVPN.OTPKey
	}
	for _, user := range inbound.ActiveUsers() {
		result.Users = append(result.Users, authUser{Name: user.Name, Password: user.Password})
	}
	return result
}

func makeOpenVPNAcceptor(runner *inboundRunner) *openvpnAcceptor {
	slot := runner.inbound.Slot()
	directory := filepath.Join(runner.manager.options.Directory, "inbound-"+strconv.Itoa(slot))
	network, _ := netip.ParsePrefix(runner.inbound.OpenVPN.Network)
	return &openvpnAcceptor{runner: runner, tools: runner.manager.options.Tools,
		directory: directory, device: devicePrefix + "i" + strconv.Itoa(slot),
		socket: filepath.Join(directory, "management.sock"), table: InboundTable(slot),
		network: network, state: makeAuthState(runner.inbound)}
}

func splitChallenge(password string) (string, string) {
	if parts := strings.Split(password, ":"); strings.HasPrefix(password, challengePrefix) &&
		len(parts) == 3 {
		secret, first := base64.StdEncoding.DecodeString(parts[1])
		code, second := base64.StdEncoding.DecodeString(parts[2])
		if first == nil && second == nil {
			return string(secret), string(code)
		}
	}
	if offset := len(password) - otpDigits; offset >= 0 {
		return password[:offset], password[offset:]
	}
	return password, ""
}

func parseClients(lines []string) []InboundClient {
	result := []InboundClient{}
	for _, line := range lines {
		fields := strings.Split(line, ",")
		// NOTE: A client without virtual address has not finished the handshake.
		if !strings.HasPrefix(line, clientListPrefix) || len(fields) < clientListFields ||
			fields[3] == "" {
			continue
		}
		client := InboundClient{Name: fields[9], Address: fields[2], Virtual: fields[3]}
		if client.Name == undefinedUser || client.Name == "" {
			client.Name = anonymousUser
		}
		client.Upload, _ = strconv.ParseInt(fields[5], 10, 64)
		client.Download, _ = strconv.ParseInt(fields[6], 10, 64)
		client.Since, _ = strconv.ParseInt(fields[8], 10, 64)
		result = append(result, client)
	}
	return result
}

func (state authState) verify(name string, password string, now time.Time) error {
	if name == "" && password == "" && state.Anonymous {
		return nil
	} else if name == "" {
		return errors.New("anonymous connections are not allowed")
	}
	index := slices.IndexFunc(state.Users, func(user authUser) bool {
		return user.Name == name
	})
	if index < 0 {
		return fmt.Errorf("unexpected user '%v'", name)
	}
	secret, code := password, ""
	if state.OTPKey != "" {
		secret, code = splitChallenge(password)
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(state.Users[index].Password)) != 1 {
		return fmt.Errorf("invalid password of user '%v'", name)
	} else if state.OTPKey == "" {
		return nil
	}
	for _, shift := range []time.Duration{0, -otpStep, otpStep} {
		if expected, err := computeOTP(state.OTPKey, now.Add(shift)); err == nil &&
			subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return nil
		}
	}
	return fmt.Errorf("invalid one-time code of user '%v'", name)
}

func (state authState) revoked(current authState) []string {
	result := []string{}
	for _, user := range state.Users {
		if !slices.Contains(current.Users, user) {
			result = append(result, user.Name)
		}
	}
	if state.Anonymous && !current.Anonymous {
		result = append(result, undefinedUser)
	}
	return result
}

func (acceptor *openvpnAcceptor) start(ctx context.Context) error {
	environment, err := acceptor.runner.manager.synchronize(ctx, acceptor.runner.logf)
	if err != nil {
		return err
	}
	path := filepath.Join(acceptor.directory, "server.conf")
	acceptor.mutex.Lock()
	state := acceptor.state
	acceptor.mutex.Unlock()
	if err = os.MkdirAll(acceptor.directory, 0o700); err != nil {
		return fmt.Errorf("failed to create directory '%v': %w", acceptor.directory, err)
	} else if err = acceptor.tools.enableForwarding(); err != nil {
		return err
	} else if err = acceptor.store(state); err != nil {
		return err
	} else if err = os.WriteFile(path, []byte(acceptor.render()), 0o600); err != nil {
		return fmt.Errorf("failed to write file '%v': %w", path, err)
	}
	acceptor.tools.removeDevice(acceptor.device)
	command := exec.Command(acceptor.tools.OpenVPN, "--config", path)
	if acceptor.runner.bypass != 0 {
		command.Args = append(command.Args, "--mark", strconv.Itoa(acceptor.runner.bypass))
	}
	if len(environment) != 0 {
		command.Env = append(os.Environ(), environment...)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	reader, writer := io.Pipe()
	command.Stdout, command.Stderr = writer, writer
	if err := command.Start(); err != nil {
		return fmt.Errorf("failed to start '%v': %w", acceptor.tools.OpenVPN, err)
	}
	ready, finished := make(chan struct{}), make(chan error, 1)
	acceptor.mutex.Lock()
	acceptor.ready, acceptor.reason = ready, ""
	acceptor.mutex.Unlock()
	acceptor.command, acceptor.finished = command, finished
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			acceptor.output(scanner.Text())
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
	timer := time.NewTimer(serverTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("server start timed out")
	case err := <-finished:
		return acceptor.explain(err)
	case <-ready:
	}
	if err := acceptor.tools.setupInbound(acceptor.table, acceptor.network.String(),
		acceptor.device); err != nil {
		return fmt.Errorf("failed to setup routing table (%v): %w", acceptor.table, err)
	}
	acceptor.tools.loosenRoute(acceptor.device)
	if err := acceptor.tools.allowForward(acceptor.device); err != nil {
		acceptor.runner.logf("forwarding rules are not installed: %v", err)
	}
	acceptor.index, acceptor.managed = 0, true
	acceptor.attach()
	return nil
}

func (acceptor *openvpnAcceptor) check(_ context.Context) error {
	acceptor.attach()
	lines, err := acceptor.manage("status 2")
	if err != nil && acceptor.managed {
		acceptor.runner.logf("management interface is not available: %v", err)
	}
	acceptor.clients, acceptor.managed = parseClients(lines), err == nil
	return nil
}

func (acceptor *openvpnAcceptor) exited() <-chan error {
	return acceptor.finished
}

func (acceptor *openvpnAcceptor) describe(status *InboundStatus) {
	status.Device, status.Address = acceptor.device, acceptor.network.Addr().Next().String()
	status.Clients = append([]InboundClient{}, acceptor.clients...)
}

func (acceptor *openvpnAcceptor) reload(inbound config.Inbound) {
	current := makeAuthState(inbound)
	acceptor.mutex.Lock()
	previous := acceptor.state
	acceptor.state = current
	acceptor.mutex.Unlock()
	if previous.Anonymous == current.Anonymous && slices.Equal(previous.Users, current.Users) {
		return
	} else if err := acceptor.store(current); err != nil {
		acceptor.runner.logf("failed to update users: %v", err)
		return
	}
	acceptor.runner.logf("users are updated: %v enabled, anonymous connections allowed: %v",
		len(current.Users), current.Anonymous)
	for _, name := range previous.revoked(current) {
		if lines, err := acceptor.manage("kill " + name); err == nil && len(lines) != 0 {
			acceptor.runner.logf("sessions of '%v': %v", name, lines[len(lines)-1])
		}
	}
}

func (acceptor *openvpnAcceptor) stop() {
	if acceptor.command != nil && acceptor.command.Process != nil {
		_ = acceptor.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-acceptor.finished:
		case <-time.After(terminationTimeout):
			_ = acceptor.command.Process.Kill()
			<-acceptor.finished
		}
		acceptor.tools.denyForward(acceptor.device)
		acceptor.tools.teardownTable(acceptor.table)
	}
	acceptor.command, acceptor.clients = nil, nil
	acceptor.tools.removeDevice(acceptor.device)
}

func (acceptor *openvpnAcceptor) dispose() {
	_ = os.RemoveAll(acceptor.directory)
}

func (acceptor *openvpnAcceptor) store(state authState) error {
	stored := state
	stored.Clock = acceptor.runner.manager.options.Clock.Path()
	data, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("failed to encode users: %w", err)
	} else if err = config.WriteFile(filepath.Join(acceptor.directory, "auth.json"),
		data); err != nil {
		return err
	}
	acceptor.mutex.Lock()
	defer acceptor.mutex.Unlock()
	acceptor.state = state
	return nil
}

func (acceptor *openvpnAcceptor) render() string {
	inbound, server := acceptor.runner.inbound, acceptor.runner.inbound.OpenVPN
	mask := net.IP(net.CIDRMask(acceptor.network.Bits(), 32)).String()
	protocol, verbosity := "udp4", server.Verbosity
	if server.Protocol == config.ProtocolTCP {
		protocol = "tcp4-server"
	}
	if inbound.Verbose {
		verbosity = max(verbosity, openvpnDebugVerbosity)
	}
	verify := acceptor.runner.manager.options.Executable + " verify " +
		filepath.Join(acceptor.directory, "auth.json")
	lines := []string{"port " + strconv.Itoa(inbound.Port), "proto " + protocol,
		"dev " + acceptor.device, "dev-type tun", "topology subnet",
		"server " + acceptor.network.Addr().String() + " " + mask,
		fmt.Sprintf("keepalive %v %v", server.KeepaliveInterval, server.KeepaliveTimeout),
		"max-clients " + strconv.Itoa(server.MaxClients),
		"reneg-sec " + strconv.Itoa(server.Renegotiation), "tun-mtu " + strconv.Itoa(server.MTU),
		"data-ciphers " + server.Ciphers, "auth " + server.Digest,
		"tls-version-min " + server.TLSVersion, "dh none", "remote-cert-tls client",
		"duplicate-cn", "username-as-common-name", "script-security 2",
		`auth-user-pass-verify "` + verify + `" via-file`, "auth-user-pass-optional",
		"tmp-dir " + acceptor.directory, "management " + acceptor.socket + " unix",
		"verb " + strconv.Itoa(verbosity)}
	if server.Listen != config.ListenAny {
		lines = append(lines, "local "+server.Listen)
	}
	if server.OTP {
		lines = append(lines, "auth-gen-token")
	}
	if server.Protocol == config.ProtocolUDP {
		lines = append(lines, "explicit-exit-notify 1")
	} else {
		// NOTE: Sets TCP_NODELAY on the server and pushes it to clients.
		lines = append(lines, "tcp-nodelay")
	}
	if server.ClientToClient {
		lines = append(lines, "client-to-client")
	}
	if server.RedirectGateway {
		lines = append(lines, `push "redirect-gateway def1"`, `push "block-outside-dns"`)
	}
	for _, route := range server.Routes {
		if prefix, err := netip.ParsePrefix(route); err == nil {
			lines = append(lines, fmt.Sprintf(`push "route %v %v"`, prefix.Addr(),
				net.IP(net.CIDRMask(prefix.Bits(), 32))))
		}
	}
	servers := server.DNS
	if len(servers) == 0 {
		servers = []string{InboundDNS}
	}
	for _, address := range servers {
		lines = append(lines, `push "dhcp-option DNS `+address+`"`)
	}
	lines = append(lines, block("ca", server.CACertificate), block("cert", server.Certificate),
		block("key", server.Key), block("tls-crypt", server.TLSCrypt))
	return strings.Join(lines, "\n") + "\n"
}

func (acceptor *openvpnAcceptor) attach() {
	device, err := net.InterfaceByName(CaptureDevice)
	if err != nil {
		acceptor.index = 0
		return
	} else if device.Index == acceptor.index {
		return
	}
	if _, err = acceptor.tools.ip("-4", "route", "replace", "default", "dev", CaptureDevice,
		"table", strconv.Itoa(acceptor.table)); err == nil {
		acceptor.index = device.Index
		acceptor.runner.logf("client traffic is attached to router device '%v'", CaptureDevice)
	}
}

func (acceptor *openvpnAcceptor) output(line string) {
	if line = strings.TrimSpace(stampPattern.ReplaceAllString(line, "")); line == "" ||
		(strings.HasPrefix(line, managementPrefix) && !strings.HasPrefix(line, managementKill)) {
		return
	}
	acceptor.runner.logf("%v", line)
	acceptor.mutex.Lock()
	defer acceptor.mutex.Unlock()
	if strings.Contains(line, optionsMarker) || strings.Contains(line, fatalMarker) {
		acceptor.reason = line
	}
	if strings.Contains(line, openvpnReady) && acceptor.ready != nil {
		close(acceptor.ready)
		acceptor.ready = nil
	}
}

func (acceptor *openvpnAcceptor) explain(err error) error {
	acceptor.mutex.Lock()
	defer acceptor.mutex.Unlock()
	if acceptor.reason != "" {
		return fmt.Errorf("%v: %w", acceptor.reason, err)
	}
	return err
}

func (acceptor *openvpnAcceptor) manage(text string) ([]string, error) {
	connection, err := net.DialTimeout("unix", acceptor.socket, managementTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to '%v': %w", acceptor.socket, err)
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(managementTimeout)); err != nil {
		return nil, fmt.Errorf("failed to set deadline of '%v': %w", acceptor.socket, err)
	} else if _, err = connection.Write([]byte(text + "\n")); err != nil {
		return nil, fmt.Errorf("failed to send command '%v': %w", text, err)
	}
	result, scanner := []string{}, bufio.NewScanner(connection)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ">") {
			continue
		}
		result = append(result, line)
		if line == "END" || strings.HasPrefix(line, "SUCCESS:") ||
			strings.HasPrefix(line, "ERROR:") {
			return result, nil
		}
	}
	return nil, fmt.Errorf("response to command '%v' is incomplete", text)
}
