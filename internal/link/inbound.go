package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"proximity/internal/config"
)

type InboundClient struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Virtual  string `json:"virtual"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Since    int64  `json:"since"`
}

type InboundStatus struct {
	State   string          `json:"state"`
	Message string          `json:"message"`
	Since   int64           `json:"since"`
	Device  string          `json:"device"`
	Address string          `json:"address"`
	Clients []InboundClient `json:"clients"`
}

type acceptor interface {
	start(ctx context.Context) error
	check(ctx context.Context) error
	exited() <-chan error
	describe(status *InboundStatus)
	reload(inbound config.Inbound)
	stop()
	dispose()
}

type inboundRunner struct {
	manager   *Manager
	inbound   config.Inbound
	signature string
	bypass    int
	key       string
	book      *logBook
	acceptor  acceptor
	cancel    context.CancelFunc
	done      chan struct{}
	mutex     sync.Mutex
	id        string
	status    InboundStatus
}

type socksAcceptor struct {
	target string
	port   int
}

type accountState struct {
	ports  []int
	active bool
}

type accountChain struct {
	name   string
	parent string
	match  string
}

const inboundRestartDelay = 3 * time.Second
const inboundRestartLimit = 30 * time.Second
const inboundCheckInterval = 3 * time.Second
const inboundAttempts = 3
const socksStartTimeout = 8 * time.Second
const socksProbeDelay = 250 * time.Millisecond
const loopbackAddress = "127.0.0.1"
const listenState = "0A"

var accountChains = []accountChain{{"PROXIMITY-STAT-IN", "INPUT", "--dport"},
	{"PROXIMITY-STAT-OUT", "OUTPUT", "--sport"}}
var socketTables = []string{"/proc/net/tcp", "/proc/net/tcp6"}
var accountPattern = regexp.MustCompile(`--[ds]port (\d+) -c \d+ (\d+)`)

func inboundSignature(inbound config.Inbound, bypass int) string {
	inbound.ID, inbound.Statistics, inbound.Autostart = "", false, false
	inbound.Outbound, inbound.Anonymous, inbound.Users = "", false, nil
	data, err := json.Marshal([]any{inbound, bypass})
	if err != nil {
		return strconv.Itoa(inbound.Port)
	}
	return string(data)
}

func portListening(port int) (bool, bool) {
	suffix, known := fmt.Sprintf(":%04X", port), false
	for _, path := range socketTables {
		data, err := os.ReadFile(path)
		known = known || err == nil
		for _, line := range strings.Split(string(data), "\n") {
			if fields := strings.Fields(line); len(fields) > 3 && fields[3] == listenState &&
				strings.HasSuffix(fields[1], suffix) {
				return true, true
			}
		}
	}
	return false, known
}

func emptyInboundStatus(state string, message string) InboundStatus {
	return InboundStatus{State: state, Message: message, Since: time.Now().Unix(),
		Clients: []InboundClient{}}
}

func (manager *Manager) ApplyInbounds(snapshot config.Config) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.snapshot = snapshot.Clone()
	manager.applyInbounds()
}

func (manager *Manager) ControlInbound(id string, action string) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	index := slices.IndexFunc(manager.snapshot.Inbounds, func(inbound config.Inbound) bool {
		return inbound.ID == id
	})
	if index < 0 {
		return fmt.Errorf("inbound '%v' does not exist", id)
	}
	inbound := manager.snapshot.Inbounds[index]
	if !inbound.Enabled {
		return fmt.Errorf("inbound '%v' is disabled", id)
	} else if problem := manager.snapshot.InboundProblem(inbound); problem != "" &&
		action != ActionStop {
		return fmt.Errorf("failed to %v inbound '%v': %v", action, id, problem)
	} else if action != ActionStop && !manager.active {
		return fmt.Errorf("failed to %v inbound '%v': %v", action, id, messageInactive)
	}
	key := inboundKey(inbound.Port)
	if current, found := manager.inbounds[inbound.Port]; found && (action != ActionStart ||
		current.snapshot().State == StateFailed) {
		current.close()
		delete(manager.inbounds, inbound.Port)
	}
	if action == ActionStop {
		manager.journal.set(key, ServiceRecord{State: StateStopped, Message: messageStopped,
			Since: time.Now().Unix()})
		manager.book(key).add(stamp("%v", messageStopped))
	} else {
		manager.journal.set(key, ServiceRecord{State: intentRunning, Since: time.Now().Unix()})
	}
	manager.applyInbounds()
	return nil
}

func (manager *Manager) Inbounds() map[string]InboundStatus {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string]InboundStatus{}
	for id, problem := range manager.blocked {
		result[id] = emptyInboundStatus(StateFailed, problem)
	}
	for _, current := range manager.inbounds {
		result[current.name()] = current.snapshot()
	}
	for _, inbound := range manager.snapshot.Inbounds {
		if _, found := result[inbound.ID]; found || !inbound.Enabled {
			continue
		}
		state, message, since := manager.dormant(inboundKey(inbound.Port))
		result[inbound.ID] = InboundStatus{State: state, Message: message, Since: since,
			Clients: []InboundClient{}}
	}
	return result
}

func (manager *Manager) Serving() map[string]bool {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string]bool{}
	for _, current := range manager.inbounds {
		result[current.name()] = true
	}
	return result
}

func (manager *Manager) applyInbounds() {
	snapshot := manager.snapshot
	wanted, keys, bypass, ports := map[int]bool{}, map[string]bool{}, 0, []int{}
	if manager.options.HostNetwork && snapshot.Settings.Capture {
		bypass = CaptureMark
	}
	manager.blocked = map[string]string{}
	for _, inbound := range snapshot.Inbounds {
		key, signature := inboundKey(inbound.Port), inboundSignature(inbound, bypass)
		problem := snapshot.InboundProblem(inbound)
		current, found := manager.inbounds[inbound.Port]
		active := inbound.Enabled && problem == "" && manager.wanted(key, true, signature, found)
		if inbound.Enabled && problem != "" {
			manager.blocked[inbound.ID] = problem
		}
		if active && inbound.Statistics && inbound.Socks5 != nil {
			ports = append(ports, inbound.Port)
		}
		if wanted[inbound.Port], keys[key] = active, inbound.Enabled; found && active &&
			current.signature == signature {
			current.refresh(inbound.Clone())
			current.acceptor.reload(inbound)
			continue
		} else if found {
			current.close()
			manager.retire(current.book, inbound.Enabled)
			delete(manager.inbounds, inbound.Port)
		}
		if active {
			manager.inbounds[inbound.Port] = manager.listen(inbound.Clone(), signature, bypass)
		}
	}
	for port, current := range manager.inbounds {
		if !wanted[port] {
			current.close()
			delete(manager.inbounds, port)
		}
	}
	manager.account.ensure(manager.options.Tools, ports)
	manager.forget(inboundPrefix, func(port int) bool {
		return slices.ContainsFunc(snapshot.Inbounds, func(inbound config.Inbound) bool {
			return inbound.Port == port
		})
	}, keys)
}

func (manager *Manager) InboundCounters() map[string][2]int64 {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string][2]int64{}
	for port, bytes := range manager.account.counters(manager.options.Tools) {
		if current, found := manager.inbounds[port]; found {
			result[current.name()] = bytes
		}
	}
	for _, current := range manager.inbounds {
		status := current.snapshot()
		if status.Device == "" || status.State != StateUp {
			continue
		}
		upload, first := readCounter(status.Device, "rx_bytes")
		download, second := readCounter(status.Device, "tx_bytes")
		if first == nil && second == nil {
			result[current.name()] = [2]int64{upload, download}
		}
	}
	return result
}

func (manager *Manager) InboundLog(id string) []string {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for _, inbound := range manager.snapshot.Inbounds {
		if inbound.ID == id {
			return manager.book(inboundKey(inbound.Port)).snapshot()
		}
	}
	return []string{}
}

func (manager *Manager) listen(inbound config.Inbound, signature string,
	bypass int) *inboundRunner {
	ctx, cancel := context.WithCancel(context.Background())
	key := inboundKey(inbound.Port)
	result := &inboundRunner{manager: manager, inbound: inbound, signature: signature,
		bypass: bypass, key: key, book: manager.book(key), cancel: cancel,
		done: make(chan struct{}), id: inbound.ID,
		status: emptyInboundStatus(StateConnecting, "")}
	result.logf("starting")
	if inbound.Type == config.TypeOpenVPN {
		result.acceptor = makeOpenVPNAcceptor(result)
	} else {
		host := inbound.Socks5.Listen
		if host == config.ListenAny {
			host = loopbackAddress
		}
		result.acceptor = &socksAcceptor{port: inbound.Port, target: net.JoinHostPort(host,
			strconv.Itoa(inbound.Port))}
	}
	go result.run(ctx)
	return result
}

func (runner *inboundRunner) run(ctx context.Context) {
	defer close(runner.done)
	defer runner.acceptor.dispose()
	failures := 0
	for ctx.Err() == nil {
		err := runner.acceptor.start(ctx)
		if err == nil {
			failures = 0
			runner.update(func(status *InboundStatus) {
				runner.acceptor.describe(status)
				status.State, status.Message = StateUp, ""
			})
			err = runner.watch(ctx)
		}
		runner.acceptor.stop()
		if ctx.Err() != nil {
			break
		}
		runner.logf("inbound failed: %v", err)
		state := StateReconnecting
		if failures++; failures >= inboundAttempts {
			state = StateFailed
		}
		runner.update(func(status *InboundStatus) {
			status.State, status.Message, status.Clients = state, err.Error(), []InboundClient{}
		})
		sleep(ctx, min(inboundRestartDelay*time.Duration(failures), inboundRestartLimit))
	}
}

func (runner *inboundRunner) watch(ctx context.Context) error {
	ticker := time.NewTicker(inboundCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-runner.acceptor.exited():
			if err == nil {
				err = errors.New("process exited")
			}
			return err
		case <-ticker.C:
		}
		if err := runner.acceptor.check(ctx); ctx.Err() != nil {
			return nil
		} else if err != nil {
			return err
		}
		runner.update(runner.acceptor.describe)
	}
}

func (runner *inboundRunner) update(change func(status *InboundStatus)) {
	runner.mutex.Lock()
	previous := runner.status.State
	if change(&runner.status); runner.status.State != previous {
		runner.status.Since = time.Now().Unix()
	}
	current := runner.status
	runner.mutex.Unlock()
	if current.State != previous {
		runner.manager.observe(runner.key, current.State, current.Message, runner.signature)
	}
}

func (runner *inboundRunner) name() string {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	return runner.id
}

func (runner *inboundRunner) refresh(inbound config.Inbound) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	runner.id = inbound.ID
}

func (runner *inboundRunner) snapshot() InboundStatus {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	result := runner.status
	result.Clients = append([]InboundClient{}, result.Clients...)
	return result
}

func (runner *inboundRunner) logf(format string, arguments ...any) {
	runner.book.add(stamp(format, arguments...))
}

func (runner *inboundRunner) close() {
	runner.cancel()
	<-runner.done
}

func (account *accountState) ensure(tools Tools, ports []int) {
	if slices.Sort(ports); slices.Equal(ports, account.ports) {
		return
	}
	account.remove(tools)
	if account.ports = ports; len(ports) == 0 {
		return
	}
	for _, chain := range accountChains {
		if _, err := tools.firewall("-N", chain.name); err != nil {
			return
		}
		for _, port := range ports {
			for _, protocol := range []string{"tcp", "udp"} {
				_, _ = tools.firewall("-A", chain.name, "-p", protocol, chain.match,
					strconv.Itoa(port))
			}
		}
		if _, err := tools.firewall("-I", chain.parent, "1", "-j", chain.name); err != nil {
			return
		}
	}
	account.active = true
}

func (account *accountState) remove(tools Tools) {
	for _, chain := range accountChains {
		for range 4 {
			if _, err := tools.firewall("-D", chain.parent, "-j", chain.name); err != nil {
				break
			}
		}
		_, _ = tools.firewall("-F", chain.name)
		_, _ = tools.firewall("-X", chain.name)
	}
	account.active, account.ports = false, nil
}

func (account *accountState) counters(tools Tools) map[int][2]int64 {
	result := map[int][2]int64{}
	if !account.active {
		return result
	}
	for index, chain := range accountChains {
		output, err := tools.firewall("-v", "-S", chain.name)
		if err != nil {
			return map[int][2]int64{}
		}
		for _, match := range accountPattern.FindAllStringSubmatch(output, -1) {
			port, _ := strconv.Atoi(match[1])
			bytes, _ := strconv.ParseInt(match[2], 10, 64)
			value := result[port]
			value[index] += bytes
			result[port] = value
		}
	}
	return result
}

func (acceptor *socksAcceptor) start(ctx context.Context) error {
	deadline := time.Now().Add(socksStartTimeout)
	for {
		err := acceptor.check(ctx)
		if err == nil || ctx.Err() != nil || time.Now().After(deadline) {
			return err
		}
		sleep(ctx, socksProbeDelay)
	}
}

func (acceptor *socksAcceptor) check(ctx context.Context) error {
	// NOTE: Socket table is preferred: probe connections would be counted as inbound traffic.
	if listening, known := portListening(acceptor.port); listening {
		return nil
	} else if known {
		return fmt.Errorf("router does not listen on port (%v)", acceptor.port)
	} else if _, err := dialProbe(ctx, acceptor.target, 0, "", defaultCheckTimeout); err != nil {
		return fmt.Errorf("listener does not accept connections: %w", err)
	}
	return nil
}

func (acceptor *socksAcceptor) exited() <-chan error {
	return nil
}

func (acceptor *socksAcceptor) describe(status *InboundStatus) {
	status.Address = acceptor.target
}

func (acceptor *socksAcceptor) reload(_ config.Inbound) {
}

func (acceptor *socksAcceptor) stop() {
}

func (acceptor *socksAcceptor) dispose() {
}
