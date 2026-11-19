package server

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/router"
	"proximity/internal/tools"
)

type probeState struct {
	port    int
	secret  string
	waiters map[int]chan string
}

type trialResult struct {
	rule    string
	chain   []string
	elapsed time.Duration
	proxied bool
	err     error
}

type toolRequest struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Count int    `json:"count"`
}

type routeCheck struct {
	Rule    string   `json:"rule"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Target  string   `json:"target"`
	Chain   []string `json:"chain"`
	Latency float64  `json:"latency"`
	Proxied bool     `json:"proxied"`
	Error   string   `json:"error"`
}

type resolverCheck struct {
	Server    string   `json:"server"`
	Addresses []string `json:"addresses"`
	Latency   float64  `json:"latency"`
	Error     string   `json:"error"`
}

type connectCheck struct {
	Latency float64 `json:"latency"`
	Proxied bool    `json:"proxied"`
	Error   string  `json:"error"`
}

type outboundCheck struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	State     string          `json:"state"`
	Resolvers []resolverCheck `json:"resolvers"`
	Connect   connectCheck    `json:"connect"`
}

type lookupResult struct {
	Host      string          `json:"host"`
	Port      int             `json:"port"`
	Route     routeCheck      `json:"route"`
	Addresses []string        `json:"addresses"`
	Problem   string          `json:"problem"`
	Outbounds []outboundCheck `json:"outbounds"`
}

type pingCheck struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	State   string        `json:"state"`
	Method  string        `json:"method"`
	Path    link.Path     `json:"path"`
	Sent    int           `json:"sent"`
	Replies []tools.Reply `json:"replies"`
	Error   string        `json:"error"`
}

type pingResult struct {
	Host      string      `json:"host"`
	Address   string      `json:"address"`
	Port      int         `json:"port"`
	Count     int         `json:"count"`
	System    string      `json:"system"`
	Blocked   bool        `json:"blocked"`
	Capture   bool        `json:"capture"`
	Outbounds []pingCheck `json:"outbounds"`
}

const probeTimeout = 8 * time.Second
const connectionPoll = 200 * time.Millisecond
const lateLine = 500 * time.Millisecond
const proxySettle = 5 * time.Second
const pingInterval = 200 * time.Millisecond
const defaultToolPort = 443
const defaultPingCount = 5
const maxPingCount = 20
const methodICMP = "icmp"
const methodTCP = "tcp"
const systemResolver = "system"
const defaultName = "Everything else"

var schemePorts = map[string]int{"http": 80, "https": 443, "ftp": 21, "ssh": 22}
var sourcePattern = regexp.MustCompile(`127\.0\.0\.1:(\d+)(?:\([^)]*\))? --> `)
var matchPattern = regexp.MustCompile(
	` --> \S+ (?:match (.+?) |doesn't match any rule )?using (.+)$`)
var failurePattern = regexp.MustCompile(`^\[TCP\] dial (.+?)(?: \(match (.*?)\))? \S+ --> \S+ ` +
	`error: (.*?)"?$`)

func makeProbe() (int, string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return 0, "", fmt.Errorf("failed to read random data: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, "", fmt.Errorf("failed to find free port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, hex.EncodeToString(buffer), nil
}

func parseChain(text string) []string {
	result := []string{}
	for _, name := range strings.FieldsFunc(text, func(symbol rune) bool {
		return symbol == '[' || symbol == ']'
	}) {
		if name = strings.TrimSpace(name); name != "" {
			result = append(result, name)
		}
	}
	return result
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

// NOTE: The way an echo request of a client takes while the host is captured: the first
// ping rule of the routes that covers the address (see router.Ping).
func systemPing(snapshot config.Config, links map[string]link.Status,
	address netip.Addr) (string, bool) {
	for _, rule := range router.Ping(snapshot, links) {
		if rule.Prefix.IsValid() && !rule.Prefix.Contains(address) {
			continue
		} else if rule.Allow {
			return config.TargetDirect, false
		}
		for _, outbound := range snapshot.Outbounds {
			if link.Table(outbound.Slot) == rule.Table && rule.Table != 0 {
				return outbound.ID, false
			}
		}
		return "", true
	}
	return config.TargetDirect, false
}

func lastOutputLine(output string) string {
	lines := slices.DeleteFunc(strings.Split(strings.TrimSpace(output), "\n"),
		func(line string) bool { return strings.TrimSpace(line) == "" })
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

func parseTrial(line string, elapsed time.Duration) trialResult {
	result := trialResult{chain: []string{}, elapsed: elapsed}
	if match := failurePattern.FindStringSubmatch(line); match != nil {
		result.rule, result.chain, result.err = readableRule(match[2]), parseChain(match[1]),
			errors.New(match[3])
	} else if match := matchPattern.FindStringSubmatch(line); match != nil {
		result.rule, result.chain = cmp.Or(match[1], "Match"), parseChain(match[2])
	}
	return result
}

// NOTE: A failed dial names its rule as "Type/payload"; shown like a success, "Type(payload)".
func readableRule(text string) string {
	if kind, payload, found := strings.Cut(text, "/"); found && payload != "" {
		return kind + "(" + payload + ")"
	} else if found {
		return kind
	}
	return text
}

func settleProxy(outbound config.Outbound, wait time.Duration) func([]string) time.Duration {
	return func([]string) time.Duration {
		if outbound.Type == config.TypeSocks5 || outbound.Type == config.TypeHTTP {
			return wait
		}
		return 0
	}
}

func (request *toolRequest) normalize() error {
	host, port := strings.TrimSpace(request.Host), 0
	if strings.Contains(host, "://") {
		address, err := url.Parse(host)
		if err != nil {
			return fail(http.StatusBadRequest, "cannot parse %q as address: %v", host, err)
		}
		host, port = address.Host, schemePorts[strings.ToLower(address.Scheme)]
	}
	if name, text, err := net.SplitHostPort(host); err == nil {
		host, port = name, 0
		if number, err := strconv.Atoi(text); err == nil {
			port = number
		}
	}
	if request.Host = strings.Trim(host, "[]"); request.Host == "" {
		return fail(http.StatusBadRequest, "host does not exist")
	} else if strings.ContainsAny(request.Host, " /\\@") {
		return fail(http.StatusBadRequest, "invalid host '%v'", request.Host)
	}
	request.Port = cmp.Or(request.Port, port, defaultToolPort)
	request.Count = min(max(cmp.Or(request.Count, defaultPingCount), 1), maxPingCount)
	if request.Port < 1 || request.Port > 65535 {
		return fail(http.StatusBadRequest, "invalid port (%v)", request.Port)
	}
	return nil
}

func (server *Server) observe(line string) {
	server.tracker.Observe(line)
	match := sourcePattern.FindStringSubmatch(line)
	if match == nil {
		return
	}
	port, _ := strconv.Atoi(match[1])
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if waiter, found := server.probe.waiters[port]; found {
		select {
		case waiter <- line:
		default:
		}
	}
}

// NOTE: A test connection asks the router to connect to the target and waits for its log line:
// mihomo answers CONNECT at once and dials afterwards, the line tells the rule, the chain and
// whether the dial succeeded. The log stream can lag behind a reload, so an open connection in
// /connections also counts. A proxy may accept before it reaches the target: settle is how
// long to wait for the far side to close such a connection.
func (server *Server) trial(ctx context.Context, proxy string, user string, target string,
	settle func(chain []string) time.Duration) trialResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	result, dialer := trialResult{chain: []string{}}, net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", proxy)
	if err != nil {
		result.err = fmt.Errorf("failed to connect to router '%v': %w", proxy, err)
		return result
	}
	defer connection.Close()
	port, waiter, closed := connection.LocalAddr().(*net.TCPAddr).Port, make(chan string, 1),
		make(chan struct{})
	server.mutex.Lock()
	server.probe.waiters[port] = waiter
	server.mutex.Unlock()
	defer func() {
		server.mutex.Lock()
		delete(server.probe.waiters, port)
		server.mutex.Unlock()
	}()
	request := fmt.Sprintf("CONNECT %v HTTP/1.1\r\nHost: %v\r\n", target, target)
	if user != "" {
		request += "Proxy-Authorization: Basic " +
			base64.StdEncoding.EncodeToString([]byte(user+":"+server.probe.secret)) + "\r\n"
	}
	started := time.Now()
	if _, err = connection.Write([]byte(request + "\r\n")); err != nil {
		result.err = fmt.Errorf("failed to send request to router: %w", err)
		return result
	}
	go func() {
		reader := bufio.NewReader(connection)
		if response, err := http.ReadResponse(reader, nil); err == nil &&
			response.StatusCode == http.StatusOK {
			_, _ = reader.ReadByte()
		}
		close(closed)
	}()
	ticker := time.NewTicker(connectionPoll)
	defer ticker.Stop()
	for reported := false; !reported; {
		select {
		case line := <-waiter:
			result, reported = parseTrial(line, time.Since(started)), true
		case <-ticker.C:
			result, reported = server.findTrial(port, time.Since(started))
		case <-closed:
			select {
			case line := <-waiter:
				result = parseTrial(line, time.Since(started))
			case <-time.After(lateLine):
				result.err = errors.New("connection closed by the router")
			}
			return result
		case <-ctx.Done():
			result.err = errors.New("router did not report the connection")
			return result
		}
	}
	if wait := settle(result.chain); result.err == nil && wait > 0 {
		result.proxied = true
		select {
		case <-closed:
			result.err = errors.New("closed by the far side: the proxy could not reach the target")
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
	return result
}

func (server *Server) findTrial(port int, elapsed time.Duration) (trialResult, bool) {
	connections, err := server.mihomo.Connections()
	if err != nil {
		return trialResult{chain: []string{}}, false
	}
	for _, connection := range connections.Items {
		if connection.Metadata.SourcePort != strconv.Itoa(port) {
			continue
		}
		result := trialResult{rule: connection.Rule, chain: slices.Clone(connection.Chains),
			elapsed: elapsed}
		if connection.Payload != "" {
			result.rule += "(" + connection.Payload + ")"
		}
		slices.Reverse(result.chain)
		return result, true
	}
	return trialResult{chain: []string{}}, false
}

func (server *Server) settleFor(snapshot config.Config,
	wait time.Duration) func([]string) time.Duration {
	return func(chain []string) time.Duration {
		if len(chain) == 0 {
			return 0
		}
		owner := router.Owner(chain[len(chain)-1])
		for _, outbound := range snapshot.Outbounds {
			if outbound.ID == owner && (outbound.Type == config.TypeSocks5 ||
				outbound.Type == config.TypeHTTP) {
				return wait
			}
		}
		return 0
	}
}

func (server *Server) probeAddress() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(server.probe.port))
}

func (server *Server) mixedAddress(settings config.Settings) string {
	host := cmp.Or(server.options.ProxyBind, settings.BindAddress)
	if address, err := netip.ParseAddr(host); err != nil || address.IsUnspecified() {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(settings.MixedPort))
}

func (server *Server) checkRoute(ctx context.Context, snapshot config.Config,
	target string) routeCheck {
	trial := server.trial(ctx, server.mixedAddress(snapshot.Settings), "", target,
		server.settleFor(snapshot, proxySettle))
	result := routeCheck{Rule: trial.rule, Chain: trial.chain,
		Latency: milliseconds(trial.elapsed), Proxied: trial.proxied}
	if trial.err != nil {
		result.Error = trial.err.Error()
	}
	if len(result.Chain) != 0 {
		result.Target = router.Owner(result.Chain[0])
	}
	if result.Key = server.originOf(result.Rule); result.Key == router.OriginDefault {
		result.Name = defaultName
	}
	for index, route := range snapshot.Routes {
		if route.Key == result.Key && result.Key != "" {
			result.Name = cmp.Or(route.ID, "Route "+strconv.Itoa(index+1))
		}
	}
	return result
}

// NOTE: The first rule of the router with the text "Type(payload)" is the one that matched.
func (server *Server) originOf(rule string) string {
	server.mutex.Lock()
	origins := server.layout.Origins
	server.mutex.Unlock()
	_, rules, err := server.mihomo.Rules()
	if err != nil || len(rules) != len(origins) || rule == "" {
		return ""
	}
	for index, item := range rules {
		text := item.Type
		if item.Payload != "" {
			text += "(" + item.Payload + ")"
		}
		if text == rule && origins[index] != "" {
			return origins[index]
		}
	}
	return ""
}

func (server *Server) probeTargets(snapshot config.Config) []config.Outbound {
	server.mutex.Lock()
	probe := server.layout.Probe
	server.mutex.Unlock()
	result := []config.Outbound{}
	if _, found := probe[config.TargetDirect]; found {
		result = append(result, config.Outbound{ID: config.TargetDirect, Type: "direct"})
	}
	for _, outbound := range snapshot.Outbounds {
		if _, found := probe[outbound.ID]; found {
			result = append(result, outbound)
		}
	}
	return result
}

func (server *Server) probeUser(id string) (string, error) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if user, found := server.layout.Probe[id]; found {
		return user, nil
	}
	return "", fmt.Errorf("outbound '%v' is not active in the router", id)
}

func (server *Server) resolveVia(ctx context.Context, text string, host string) resolverCheck {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	result, started := resolverCheck{Server: text, Addresses: []string{}}, time.Now()
	serverText, exit, _ := strings.Cut(text, "#")
	var addresses []string
	var err error
	if serverText == systemResolver {
		addresses, err = net.DefaultResolver.LookupHost(ctx, host)
	} else if parsed, problem := config.ParseDNSServer(serverText); problem != nil {
		err = problem
	} else if user, problem := server.probeUser(cmp.Or(exit, config.TargetDirect)); problem != nil {
		err = problem
	} else {
		addresses, err = tools.Lookup(ctx, tools.ProxyDial(server.probeAddress(), user,
			server.probe.secret), parsed, host)
	}
	if result.Latency = milliseconds(time.Since(started)); err != nil {
		result.Error = err.Error()
	} else if len(addresses) == 0 {
		result.Error = "no addresses"
	} else {
		result.Addresses = addresses
	}
	return result
}

func (server *Server) lookup(request *http.Request, _ session) (any, error) {
	body := toolRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = body.normalize(); err != nil {
		return nil, err
	} else if !server.mihomo.State().Running {
		return nil, fail(http.StatusConflict, "router is not running")
	}
	return server.inspect(request.Context(), body), nil
}

func (server *Server) inspect(ctx context.Context, body toolRequest) lookupResult {
	snapshot, links := server.store.Snapshot(), server.manager.Status()
	target := net.JoinHostPort(body.Host, strconv.Itoa(body.Port))
	result := lookupResult{Host: body.Host, Port: body.Port, Addresses: []string{},
		Outbounds: []outboundCheck{}}
	resolvers := router.Resolvers(snapshot, links)
	targets := server.probeTargets(snapshot)
	result.Outbounds = make([]outboundCheck, len(targets))
	group := sync.WaitGroup{}
	group.Go(func() {
		result.Route = server.checkRoute(ctx, snapshot, target)
	})
	group.Go(func() {
		if _, err := netip.ParseAddr(body.Host); err == nil {
			result.Addresses = []string{body.Host}
		} else if addresses, err := server.mihomo.Resolve(ctx, body.Host); err != nil {
			result.Problem = err.Error()
		} else {
			result.Addresses = addresses
		}
	})
	for index, outbound := range targets {
		servers := resolvers[outbound.ID]
		if len(servers) == 0 {
			servers = []string{systemResolver}
		}
		group.Go(func() {
			result.Outbounds[index] = server.checkOutbound(ctx, outbound, links, servers,
				body.Host, target)
		})
	}
	group.Wait()
	return result
}

func (server *Server) checkOutbound(ctx context.Context, outbound config.Outbound,
	links map[string]link.Status, resolvers []string, host string, target string) outboundCheck {
	result := outboundCheck{ID: outbound.ID, Type: outbound.Type, State: links[outbound.ID].State,
		Resolvers: make([]resolverCheck, len(resolvers))}
	if outbound.ID == config.TargetDirect {
		result.State = link.StateUp
	}
	group := sync.WaitGroup{}
	if _, err := netip.ParseAddr(host); err != nil {
		for index, text := range resolvers {
			group.Go(func() {
				result.Resolvers[index] = server.resolveVia(ctx, text, host)
			})
		}
	} else {
		result.Resolvers = []resolverCheck{}
	}
	group.Go(func() {
		user, err := server.probeUser(outbound.ID)
		if err != nil {
			result.Connect.Error = err.Error()
			return
		}
		trial := server.trial(ctx, server.probeAddress(), user, target, settleProxy(outbound,
			proxySettle))
		if result.Connect.Proxied = trial.proxied; trial.err != nil {
			result.Connect.Error = trial.err.Error()
		} else {
			result.Connect.Latency = milliseconds(trial.elapsed)
		}
	})
	group.Wait()
	return result
}

func (server *Server) ping(request *http.Request, _ session) (any, error) {
	body := toolRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = body.normalize(); err != nil {
		return nil, err
	} else if !server.mihomo.State().Running {
		return nil, fail(http.StatusConflict, "router is not running")
	}
	address, err := netip.ParseAddr(body.Host)
	if err != nil {
		addresses, err := server.mihomo.Resolve(request.Context(), body.Host)
		if err != nil {
			return nil, fail(http.StatusBadRequest, "%v", err)
		}
		address, _ = netip.ParseAddr(addresses[0])
	}
	return server.pingAll(request.Context(), body, address), nil
}

func (server *Server) pingAll(ctx context.Context, body toolRequest,
	address netip.Addr) pingResult {
	snapshot, links := server.store.Snapshot(), server.manager.Status()
	result := pingResult{Host: body.Host, Address: address.String(), Port: body.Port,
		Count: body.Count, Capture: server.options.HostNetwork && snapshot.Settings.Capture}
	result.System, result.Blocked = systemPing(snapshot, links, address)
	targets := server.probeTargets(snapshot)
	result.Outbounds = make([]pingCheck, len(targets))
	group := sync.WaitGroup{}
	for index, outbound := range targets {
		group.Go(func() {
			result.Outbounds[index] = server.pingVia(ctx, outbound, links, address, body.Port,
				body.Count)
		})
	}
	group.Wait()
	return result
}

func (server *Server) pingVia(ctx context.Context, outbound config.Outbound,
	links map[string]link.Status, address netip.Addr, port int, count int) pingCheck {
	result := pingCheck{ID: outbound.ID, Type: outbound.Type, State: links[outbound.ID].State,
		Method: methodICMP, Sent: count, Replies: []tools.Reply{}}
	if outbound.ID == config.TargetDirect {
		result.State = link.StateUp
	}
	path, err := server.manager.Path(outbound.ID)
	if err != nil {
		result.Error = err.Error()
		return result
	} else if result.Path = path; path.Proxy {
		result.Method = methodTCP
		return server.pingTCP(ctx, outbound, result, net.JoinHostPort(address.String(),
			strconv.Itoa(port)))
	}
	arguments := []string{"-n", "-c", strconv.Itoa(count), "-i", "0.2", "-W", "1"}
	if path.Mark != 0 {
		arguments = append(arguments, "-m", strconv.Itoa(path.Mark))
	}
	if path.Device != "" {
		arguments = append(arguments, "-I", path.Device)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(count)*time.Second+probeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, cmp.Or(server.options.Tools.Ping, "ping"),
		append(arguments, address.String())...).CombinedOutput()
	if result.Replies = tools.ParsePing(string(output)); len(result.Replies) == 0 && err != nil {
		result.Error = cmp.Or(lastOutputLine(string(output)), err.Error())
	}
	return result
}

func (server *Server) pingTCP(ctx context.Context, outbound config.Outbound, result pingCheck,
	target string) pingCheck {
	user, err := server.probeUser(result.ID)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	trials, group := make([]trialResult, result.Sent), sync.WaitGroup{}
	for index := range result.Sent {
		group.Go(func() {
			select {
			case <-time.After(time.Duration(index) * pingInterval):
				trials[index] = server.trial(ctx, server.probeAddress(), user, target,
					settleProxy(outbound, proxySettle))
			case <-ctx.Done():
				trials[index] = trialResult{err: ctx.Err()}
			}
		})
	}
	group.Wait()
	for index, trial := range trials {
		if trial.err != nil {
			result.Error = trial.err.Error()
		} else {
			result.Replies = append(result.Replies, tools.Reply{Sequence: index + 1,
				Time: milliseconds(trial.elapsed)})
		}
	}
	if len(result.Replies) != 0 {
		result.Error = ""
	}
	return result
}
