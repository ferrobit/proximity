package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
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
	Executable  string
	Tools       Tools
	HostNetwork bool
	Changed     func()
}

type Manager struct {
	mutex   sync.Mutex
	options Options
	runners map[int]*runner
	capture captureState
}

type runner struct {
	manager   *Manager
	endpoint  config.Endpoint
	settings  config.Settings
	signature string
	bypass    int
	cancel    context.CancelFunc
	done      chan struct{}
	mutex     sync.Mutex
	id        string
	status    Status
	lines     []string
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
)

const maxLogLines = 300
const defaultCheckTimeout = 5 * time.Second
const monitoringGrace = 3 * time.Second

func NewManager(options Options) (*Manager, error) {
	if err := os.MkdirAll(options.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create directory '%v': %w", options.Directory, err)
	} else if err = options.Tools.cleanup(); err != nil {
		return nil, fmt.Errorf("failed to cleanup network state: %w", err)
	}
	return &Manager{options: options, runners: map[int]*runner{},
		capture: captureState{device: CaptureDevice}}, nil
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

func (manager *Manager) Apply(endpoints []config.Endpoint, settings config.Settings) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	wanted, bypass := map[int]bool{}, 0
	if manager.options.HostNetwork && settings.Capture {
		bypass = CaptureMark
	}
	for _, endpoint := range endpoints {
		signature := makeSignature(endpoint, settings, bypass)
		wanted[endpoint.Slot] = endpoint.Enabled
		if current, found := manager.runners[endpoint.Slot]; found && endpoint.Enabled &&
			current.signature == signature {
			current.rename(endpoint.ID)
			continue
		} else if found {
			current.close()
			delete(manager.runners, endpoint.Slot)
		}
		if endpoint.Enabled {
			manager.runners[endpoint.Slot] = manager.launch(endpoint, settings, signature, bypass)
		}
	}
	for slot, current := range manager.runners {
		if !wanted[slot] {
			current.close()
			delete(manager.runners, slot)
		}
	}
}

func (manager *Manager) Restart(id string) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	current := manager.find(id)
	if current == nil {
		return fmt.Errorf("endpoint '%v' is not running", id)
	}
	current.close()
	endpoint := current.endpoint
	endpoint.ID = id
	manager.runners[endpoint.Slot] = manager.launch(endpoint, current.settings,
		current.signature, current.bypass)
	return nil
}

func (manager *Manager) Status() map[string]Status {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string]Status{}
	for _, current := range manager.runners {
		result[current.name()] = current.snapshot()
	}
	return result
}

func (manager *Manager) Counters() map[string][2]int64 {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string][2]int64{}
	for _, current := range manager.runners {
		status := current.snapshot()
		if !current.endpoint.IsTunnel() || status.State != StateUp {
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
	if current := manager.find(id); current != nil {
		current.mutex.Lock()
		defer current.mutex.Unlock()
		return append([]string{}, current.lines...)
	}
	return []string{}
}

func (manager *Manager) Close() {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for slot, current := range manager.runners {
		current.close()
		delete(manager.runners, slot)
	}
	manager.capture.remove(manager.options.Tools)
}

func (manager *Manager) find(id string) *runner {
	for _, current := range manager.runners {
		if current.name() == id {
			return current
		}
	}
	return nil
}

func (manager *Manager) launch(endpoint config.Endpoint, settings config.Settings,
	signature string, bypass int) *runner {
	ctx, cancel := context.WithCancel(context.Background())
	result := &runner{manager: manager, endpoint: endpoint, settings: settings,
		signature: signature, bypass: bypass, cancel: cancel, done: make(chan struct{}),
		id: endpoint.ID, status: Status{State: StateConnecting, Since: time.Now().Unix(),
			Latency: -1, DNS: []string{}, Domains: []string{}, Routes: []string{}}}
	go result.run(ctx)
	return result
}

func makeSignature(endpoint config.Endpoint, settings config.Settings, bypass int) string {
	endpoint.ID, endpoint.Name, endpoint.Group, endpoint.Statistics = "", "", "", false
	if endpoint.IsTunnel() && endpoint.Monitoring != nil && endpoint.Monitoring.Enabled {
		settings = config.Settings{}
	}
	data, err := json.Marshal([]any{endpoint, settings.ProbeTarget, settings.CheckInterval,
		settings.ConnectivityChecks, bypass})
	if err != nil {
		return strconv.Itoa(endpoint.Slot)
	}
	return string(data)
}

func (runner *runner) policy() policy {
	interval := time.Duration(runner.settings.CheckInterval) * time.Second
	if monitoring := runner.endpoint.Monitoring; runner.endpoint.IsTunnel() {
		return policy{limit: monitoring.ReconnectAttempts + 1, exhausted: StateFailed,
			delay: time.Duration(monitoring.ReconnectDelay) * time.Second, terminal: true}
	} else if host := runner.endpoint.Host; host != nil {
		return policy{limit: host.Attempts, exhausted: StateFailed,
			delay:  time.Duration(host.ReconnectTimeout) * time.Second,
			verify: runner.settings.ConnectivityChecks}
	}
	return policy{limit: 1, delay: interval, exhausted: StateDown,
		verify: runner.settings.ConnectivityChecks}
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
		err := driver.start(ctx)
		if err == nil && policy.verify {
			latency, err = runner.probe(ctx, driver, defaultCheckTimeout, 1)
		}
		if err == nil {
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
			sleep(ctx, policy.delay)
		}
	}
}

func (runner *runner) watch(ctx context.Context, driver driver, verified bool,
	healthy func()) error {
	monitoring := runner.endpoint.Monitoring
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
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, arguments...)
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	if runner.lines = append(runner.lines, line); len(runner.lines) > maxLogLines {
		runner.lines = runner.lines[len(runner.lines)-maxLogLines:]
	}
}

func (runner *runner) close() {
	runner.cancel()
	<-runner.done
}

func sameStatus(first Status, second Status) bool {
	return first.State == second.State && fmt.Sprint(first.DNS, first.Domains, first.Routes) ==
		fmt.Sprint(second.DNS, second.Domains, second.Routes)
}
