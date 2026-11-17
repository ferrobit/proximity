package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"proximity/internal/config"
)

type Status struct {
	State   string   `json:"state"`
	Message string   `json:"message"`
	Since   int64    `json:"since"`
	Link    string   `json:"link"`
	Latency int      `json:"latency"`
	Checked int64    `json:"checked"`
	Device  string   `json:"device"`
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`
	Domains []string `json:"domains"`
	Routes  []string `json:"routes"`
}

type Options struct {
	Directory   string
	StateDir    string
	Executable  string
	Tools       Tools
	HostNetwork bool
	Changed     func()
}

type Manager struct {
	mutex     sync.Mutex
	options   Options
	runners   map[int]*runner
	inbounds  map[int]*inboundRunner
	blocked   map[string]string
	capture   captureState
	account   accountState
	journal   *journal
	books     map[string]*logBook
	outbounds []config.Outbound
	settings  config.Settings
	snapshot  config.Config
	active    bool
	changed   int64
}

type runner struct {
	manager   *Manager
	outbound  config.Outbound
	settings  config.Settings
	signature string
	bypass    int
	key       string
	book      *logBook
	cancel    context.CancelFunc
	done      chan struct{}
	mutex     sync.Mutex
	id        string
	status    Status
}

type policy struct {
	limit     int
	delay     time.Duration
	exhausted string
	terminal  bool
	verify    bool
}

type fatalError struct {
	cause error
}

const (
	StateDisabled     = "disabled"
	StateConnecting   = "connecting"
	StateUp           = "up"
	StateDown         = "down"
	StateReconnecting = "reconnecting"
	StateFailed       = "failed"
	StateStopped      = "stopped"
)

const maxLogLines = 300
const maxVerboseLines = 3000
const debugPrefix = "debug: "
const maxTracedLines = 20
const defaultCheckTimeout = 5 * time.Second
const monitoringGrace = 3 * time.Second

func NewManager(options Options) (*Manager, error) {
	if err := os.MkdirAll(options.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create directory '%v': %w", options.Directory, err)
	} else if err = options.Tools.cleanup(); err != nil {
		return nil, fmt.Errorf("failed to cleanup network state: %w", err)
	}
	journal, err := openJournal(options.StateDir)
	if err != nil {
		return nil, err
	}
	return &Manager{options: options, runners: map[int]*runner{},
		inbounds: map[int]*inboundRunner{}, blocked: map[string]string{},
		capture: captureState{device: CaptureDevice}, journal: journal,
		books: map[string]*logBook{}, outbounds: []config.Outbound{},
		changed: time.Now().Unix()}, nil
}

func sleep(ctx context.Context, duration time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}
}

func (failure *fatalError) Error() string {
	return failure.cause.Error()
}

func (failure *fatalError) Unwrap() error {
	return failure.cause
}

func (manager *Manager) SetActive(active bool, snapshot config.Config) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if active && !manager.active {
		for _, outbound := range snapshot.Outbounds {
			if outbound.Enabled {
				manager.autostart(outboundKey(outbound.Slot), outbound.Autostart)
			}
		}
		for _, inbound := range snapshot.Inbounds {
			if inbound.Enabled {
				manager.autostart(inboundKey(inbound.Port), inbound.Autostart)
			}
		}
	}
	if active != manager.active {
		manager.active, manager.changed = active, time.Now().Unix()
	}
}

func (manager *Manager) Apply(outbounds []config.Outbound, settings config.Settings) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.outbounds, manager.settings = slices.Clone(outbounds), settings
	manager.applyOutbounds()
}

func (manager *Manager) Control(id string, action string) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	index := slices.IndexFunc(manager.outbounds, func(outbound config.Outbound) bool {
		return outbound.ID == id
	})
	if index < 0 {
		return fmt.Errorf("outbound '%v' does not exist", id)
	}
	outbound := manager.outbounds[index]
	if !outbound.Enabled {
		return fmt.Errorf("outbound '%v' is disabled", id)
	} else if action != ActionStop && !manager.active {
		return fmt.Errorf("failed to %v outbound '%v': %v", action, id, messageInactive)
	}
	key := outboundKey(outbound.Slot)
	if current, found := manager.runners[outbound.Slot]; found && (action != ActionStart ||
		current.snapshot().State == StateFailed) {
		current.close()
		delete(manager.runners, outbound.Slot)
	}
	if action == ActionStop {
		manager.journal.set(key, ServiceRecord{State: StateStopped, Message: messageStopped,
			Since: time.Now().Unix()})
		manager.book(key).add(stamp("%v", messageStopped))
	} else {
		manager.journal.set(key, ServiceRecord{State: intentRunning, Since: time.Now().Unix()})
	}
	manager.applyOutbounds()
	return nil
}

func (manager *Manager) Status() map[string]Status {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string]Status{}
	for _, current := range manager.runners {
		result[current.name()] = current.snapshot()
	}
	for _, outbound := range manager.outbounds {
		if _, found := manager.runners[outbound.Slot]; found || !outbound.Enabled {
			continue
		}
		state, message, since := manager.dormant(outboundKey(outbound.Slot))
		result[outbound.ID] = Status{State: state, Message: message, Since: since, Latency: -1,
			DNS: []string{}, Domains: []string{}, Routes: []string{}}
	}
	return result
}

func (manager *Manager) Counters() map[string][2]int64 {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string][2]int64{}
	for _, current := range manager.runners {
		status := current.snapshot()
		if !current.outbound.IsTunnel() || status.State != StateUp {
			continue
		}
		upload, first := readCounter(status.Device, "tx_bytes")
		download, second := readCounter(status.Device, "rx_bytes")
		if first == nil && second == nil {
			result[current.name()] = [2]int64{upload, download}
		}
	}
	return result
}

func (manager *Manager) Log(id string) []string {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for _, outbound := range manager.outbounds {
		if outbound.ID == id {
			return manager.book(outboundKey(outbound.Slot)).snapshot()
		}
	}
	return []string{}
}

func (manager *Manager) TraceOutbound(id string, lines []string) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for _, outbound := range manager.outbounds {
		if outbound.ID == id && outbound.Verbose {
			trace(manager.book(outboundKey(outbound.Slot)), lines)
		}
	}
}

func (manager *Manager) Close() {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for port, current := range manager.inbounds {
		current.close()
		current.book.add(stamp("stopped: Proximity is shutting down"))
		delete(manager.inbounds, port)
	}
	manager.account.remove(manager.options.Tools)
	for slot, current := range manager.runners {
		current.close()
		current.book.add(stamp("stopped: Proximity is shutting down"))
		delete(manager.runners, slot)
	}
	manager.capture.remove(manager.options.Tools)
}

func (manager *Manager) applyOutbounds() {
	wanted, keys, bypass := map[int]bool{}, map[string]bool{}, 0
	if manager.options.HostNetwork && manager.settings.Capture {
		bypass = CaptureMark
	}
	for _, outbound := range manager.outbounds {
		key, signature := outboundKey(outbound.Slot), makeSignature(outbound, manager.settings, bypass)
		current, found := manager.runners[outbound.Slot]
		active := outbound.Enabled && manager.wanted(key, signature, found)
		if wanted[outbound.Slot], keys[key] = active, outbound.Enabled; found && active &&
			current.signature == signature {
			current.rename(outbound.ID)
			continue
		} else if found {
			current.close()
			manager.retire(current.book, outbound.Enabled)
			delete(manager.runners, outbound.Slot)
		}
		if active {
			manager.runners[outbound.Slot] = manager.launch(outbound, manager.settings, signature,
				bypass)
		}
	}
	for slot, current := range manager.runners {
		if !wanted[slot] {
			current.close()
			delete(manager.runners, slot)
		}
	}
	manager.forget(outboundPrefix, func(slot int) bool {
		return slices.ContainsFunc(manager.outbounds, func(outbound config.Outbound) bool {
			return outbound.Slot == slot
		})
	}, keys)
}

func (manager *Manager) wanted(key string, signature string, running bool) bool {
	if !manager.active {
		return false
	}
	record, found := manager.journal.get(key)
	if !found || record.State == intentRunning {
		return true
	} else if record.State == StateFailed && record.Signature != digest(signature) {
		manager.journal.set(key, ServiceRecord{State: intentRunning, Since: time.Now().Unix()})
		return true
	}
	return record.State == StateFailed && running
}

func (manager *Manager) autostart(key string, enabled bool) {
	if record, found := manager.journal.get(key); enabled || (found &&
		record.State != intentRunning) {
		return
	}
	manager.journal.set(key, ServiceRecord{State: StateStopped, Message: messageAutostart,
		Since: time.Now().Unix()})
	manager.book(key).add(stamp("%v", messageAutostart))
}

func (manager *Manager) dormant(key string) (string, string, int64) {
	if record, found := manager.journal.get(key); found && record.State != intentRunning {
		return record.State, record.Message, record.Since
	}
	return StateStopped, messageInactive, manager.changed
}

func (manager *Manager) retire(book *logBook, service bool) {
	if service && !manager.active {
		book.add(stamp("stopped: %v", messageInactive))
	}
}

func (manager *Manager) forget(prefix string, exists func(slot int) bool, keys map[string]bool) {
	manager.journal.remove(func(key string) bool {
		return !strings.HasPrefix(key, prefix) || keys[key]
	})
	for key := range manager.books {
		if slot, err := strconv.Atoi(strings.TrimPrefix(key, prefix)); err == nil &&
			strings.HasPrefix(key, prefix) && !exists(slot) {
			delete(manager.books, key)
		}
	}
	if manager.options.StateDir == "" {
		return
	}
	directory := filepath.Join(manager.options.StateDir, logsDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), logSuffix)
		if slot, err := strconv.Atoi(strings.TrimPrefix(name, prefix)); err == nil &&
			strings.HasPrefix(name, prefix) && !exists(slot) {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
}

func (manager *Manager) book(key string) *logBook {
	if current, found := manager.books[key]; found {
		return current
	}
	path := ""
	if manager.options.StateDir != "" {
		path = filepath.Join(manager.options.StateDir, logsDirectory, key+logSuffix)
	}
	result := openLogBook(path)
	manager.books[key] = result
	return result
}

func (manager *Manager) launch(outbound config.Outbound, settings config.Settings,
	signature string, bypass int) *runner {
	ctx, cancel := context.WithCancel(context.Background())
	key := outboundKey(outbound.Slot)
	result := &runner{manager: manager, outbound: outbound, settings: settings,
		signature: signature, bypass: bypass, key: key, book: manager.book(key), cancel: cancel,
		done: make(chan struct{}), id: outbound.ID, status: Status{State: StateConnecting,
			Since: time.Now().Unix(), Latency: -1, DNS: []string{}, Domains: []string{},
			Routes: []string{}}}
	result.book.resize(outbound.Verbose)
	result.logf("starting")
	result.debugSettings()
	go result.run(ctx)
	return result
}

func makeSignature(outbound config.Outbound, settings config.Settings, bypass int) string {
	outbound.ID, outbound.Statistics, outbound.Autostart = "", false, false
	if outbound.IsTunnel() && outbound.Monitoring != nil && outbound.Monitoring.Enabled {
		settings = config.Settings{}
	}
	data, err := json.Marshal([]any{outbound, settings.ProbeTarget, settings.CheckInterval,
		settings.ConnectivityChecks, bypass})
	if err != nil {
		return strconv.Itoa(outbound.Slot)
	}
	return string(data)
}

func (runner *runner) policy() policy {
	result := policy{limit: max(1, runner.outbound.Attempts), exhausted: StateFailed,
		delay: time.Duration(runner.outbound.ReconnectTimeout) * time.Second}
	if result.terminal = runner.outbound.IsTunnel(); !result.terminal {
		result.verify = runner.settings.ConnectivityChecks
	}
	return result
}

func (runner *runner) run(ctx context.Context) {
	defer close(runner.done)
	driver := makeDriver(runner)
	defer driver.dispose()
	policy := runner.policy()
	failures, state, message := 0, StateConnecting, ""
	for ctx.Err() == nil {
		runner.update(func(status *Status) {
			status.State, status.Message = state, message
		})
		latency := time.Duration(-1)
		runner.debugf("starting the link, attempt %v of %v", failures+1, policy.limit)
		err := driver.start(ctx)
		if err == nil && policy.verify {
			latency, err = runner.probe(ctx, driver, defaultCheckTimeout, 1)
		}
		if err == nil {
			runner.debugf("the link is started")
			runner.update(func(status *Status) {
				driver.describe(status)
				status.State, status.Message = StateUp, ""
				if latency >= 0 {
					status.Checked, status.Latency = time.Now().Unix(), int(latency.Milliseconds())
				}
			})
			err = runner.watch(ctx, driver, policy.verify, func() {
				failures = 0
			})
		}
		driver.stop()
		if ctx.Err() != nil {
			break
		}
		fatal := &fatalError{}
		runner.logf("link failed: %v", err)
		if failures++; errors.As(err, &fatal) || failures >= policy.limit {
			if failures <= policy.limit {
				runner.logf("connection failed after %v of %v attempts", failures, policy.limit)
			}
			state, message = policy.exhausted, err.Error()
			if runner.fail(state, message); policy.terminal {
				<-ctx.Done()
			} else {
				sleep(ctx, time.Duration(runner.settings.CheckInterval)*time.Second)
			}
		} else {
			state = StateReconnecting
			message = fmt.Sprintf("reconnect attempt %v of %v: %v", failures, policy.limit-1, err)
			runner.fail(state, message)
			runner.debugf("next attempt in %v", policy.delay)
			sleep(ctx, policy.delay)
		}
	}
}

func (runner *runner) watch(ctx context.Context, driver driver, verified bool,
	healthy func()) error {
	monitoring := runner.outbound.Monitoring
	active := monitoring != nil && monitoring.Enabled
	interval, timeout, retries := time.Duration(runner.settings.CheckInterval)*time.Second,
		defaultCheckTimeout, 1
	timer := time.NewTimer(interval)
	defer timer.Stop()
	if active {
		interval, timeout = time.Duration(monitoring.Interval)*time.Second,
			time.Duration(monitoring.Timeout)*time.Second
		retries = monitoring.Retries + 1
		timer.Reset(min(interval, monitoringGrace))
	} else if healthy(); !runner.settings.ConnectivityChecks {
		timer.Stop()
	} else if !verified {
		timer.Reset(0)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-driver.exited():
			if err == nil {
				err = errors.New("process exited")
			}
			return err
		case <-timer.C:
		}
		latency, err := runner.probe(ctx, driver, timeout, retries)
		if ctx.Err() != nil {
			return nil
		} else if err != nil && active {
			return fmt.Errorf("monitoring failed after %v attempts: %w", retries, err)
		} else if err != nil && verified {
			return err
		}
		runner.update(func(status *Status) {
			status.Checked, status.Latency, status.Message = time.Now().Unix(), -1, ""
			if err == nil {
				healthy()
				status.Latency = int(latency.Milliseconds())
			} else {
				status.Message = err.Error()
			}
		})
		timer.Reset(interval)
	}
}

func (runner *runner) probe(ctx context.Context, driver driver, timeout time.Duration,
	retries int) (time.Duration, error) {
	var failure error
	for attempt := range retries {
		latency, err := driver.check(ctx, timeout)
		if err == nil {
			runner.debugf("connectivity check %v of %v passed in %v ms", attempt+1, retries,
				latency.Milliseconds())
		}
		if err == nil || ctx.Err() != nil {
			return latency, err
		}
		failure = err
		runner.logf("connectivity check %v of %v failed: %v", attempt+1, retries, err)
	}
	return 0, failure
}

func (runner *runner) fail(state string, message string) {
	runner.update(func(status *Status) {
		status.State, status.Message, status.Latency = state, message, -1
		status.Address, status.Gateway = "", ""
	})
}

func (runner *runner) update(change func(status *Status)) {
	runner.mutex.Lock()
	previous := runner.status
	change(&runner.status)
	if runner.status.Device != "" {
		runner.status.Link = linkState(runner.status.Device)
	}
	if runner.status.State != previous.State {
		runner.status.Since = time.Now().Unix()
	}
	current := runner.status
	runner.mutex.Unlock()
	if current.State != previous.State {
		runner.manager.observe(runner.key, current.State, current.Message, runner.signature)
		runner.debugf("state %v -> %v", previous.State, current.State)
	}
	if !sameStatus(previous, current) && runner.manager.options.Changed != nil {
		runner.manager.options.Changed()
	}
}

func (runner *runner) name() string {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	return runner.id
}

func (runner *runner) rename(id string) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	runner.id = id
}

func (runner *runner) snapshot() Status {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	return runner.status
}

func (runner *runner) logf(format string, arguments ...any) {
	runner.book.add(stamp(format, arguments...))
}

func (runner *runner) debugf(format string, arguments ...any) {
	if runner.outbound.Verbose {
		runner.book.add(stamp(debugPrefix+format, arguments...))
	}
}

func (runner *runner) debugSettings() {
	outbound, settings := runner.outbound, runner.settings
	runner.debugf("type %v, attempts %v, reconnect timeout %v s, statistics %v", outbound.Type,
		outbound.Attempts, outbound.ReconnectTimeout, outbound.Statistics)
	if monitoring := outbound.Monitoring; outbound.IsTunnel() && monitoring != nil &&
		monitoring.Enabled {
		runner.debugf("monitoring every %v s, timeout %v s, retries %v, target '%v'",
			monitoring.Interval, monitoring.Timeout, monitoring.Retries, monitoring.Target)
	} else if outbound.IsTunnel() {
		runner.debugf("monitoring is off")
	} else {
		runner.debugf("connectivity checks %v, every %v s, probe target '%v'",
			settings.ConnectivityChecks, settings.CheckInterval, settings.ProbeTarget)
	}
}

func (runner *runner) close() {
	runner.cancel()
	<-runner.done
}

func (manager *Manager) observe(key string, state string, message string, signature string) {
	if state == StateUp {
		manager.journal.set(key, ServiceRecord{State: intentRunning, Since: time.Now().Unix()})
	} else if state == StateFailed {
		manager.journal.set(key, ServiceRecord{State: StateFailed, Message: message,
			Since: time.Now().Unix(), Signature: digest(signature)})
	}
}

func trace(book *logBook, lines []string) {
	for index, line := range lines {
		if index == maxTracedLines {
			book.add(stamp(debugPrefix+"%v more connections are not shown", len(lines)-index))
			return
		}
		book.add(stamp(debugPrefix+"%v", line))
	}
}

func sameStatus(first Status, second Status) bool {
	return first.State == second.State && fmt.Sprint(first.DNS, first.Domains, first.Routes) ==
		fmt.Sprint(second.DNS, second.Domains, second.Routes)
}
