package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	Directory  string
	Executable string
	Tools      Tools
	Changed    func()
}

type Manager struct {
	mutex   sync.Mutex
	options Options
	runners map[string]*runner
}

type runner struct {
	manager   *Manager
	endpoint  config.Endpoint
	settings  config.Settings
	signature string
	cancel    context.CancelFunc
	done      chan struct{}
	mutex     sync.Mutex
	status    Status
	lines     []string
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
	return &Manager{options: options, runners: map[string]*runner{}}, nil
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
	wanted := map[string]bool{}
	for _, endpoint := range endpoints {
		signature := makeSignature(endpoint, settings)
		wanted[endpoint.ID] = endpoint.Enabled
		if current, found := manager.runners[endpoint.ID]; found && endpoint.Enabled &&
			current.signature == signature {
			continue
		} else if found {
			current.close()
			delete(manager.runners, endpoint.ID)
		}
		if endpoint.Enabled {
			manager.runners[endpoint.ID] = manager.launch(endpoint, settings, signature)
		}
	}
	for id, current := range manager.runners {
		if !wanted[id] {
			current.close()
			delete(manager.runners, id)
		}
	}
}

func (manager *Manager) Restart(id string) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	current, found := manager.runners[id]
	if !found {
		return fmt.Errorf("endpoint '%v' is not running", id)
	}
	current.close()
	manager.runners[id] = manager.launch(current.endpoint, current.settings, current.signature)
	return nil
}

func (manager *Manager) Status() map[string]Status {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string]Status{}
	for id, current := range manager.runners {
		result[id] = current.snapshot()
	}
	return result
}

func (manager *Manager) Counters() map[string][2]int64 {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	result := map[string][2]int64{}
	for id, current := range manager.runners {
		status := current.snapshot()
		if !current.endpoint.IsTunnel() || status.State != StateUp {
			continue
		}
		upload, first := readCounter(status.Device, "tx_bytes")
		download, second := readCounter(status.Device, "rx_bytes")
		if first == nil && second == nil {
			result[id] = [2]int64{upload, download}
		}
	}
	return result
}

func (manager *Manager) Log(id string) []string {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	if current, found := manager.runners[id]; found {
		current.mutex.Lock()
		defer current.mutex.Unlock()
		return append([]string{}, current.lines...)
	}
	return []string{}
}

func (manager *Manager) Close() {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	for id, current := range manager.runners {
		current.close()
		delete(manager.runners, id)
	}
}

func (manager *Manager) launch(endpoint config.Endpoint, settings config.Settings,
	signature string) *runner {
	ctx, cancel := context.WithCancel(context.Background())
	result := &runner{manager: manager, endpoint: endpoint, settings: settings,
		signature: signature, cancel: cancel, done: make(chan struct{}),
		status: Status{State: StateConnecting, Since: time.Now().Unix(), Latency: -1,
			DNS: []string{}, Domains: []string{}, Routes: []string{}}}
	go result.run(ctx)
	return result
}

func makeSignature(endpoint config.Endpoint, settings config.Settings) string {
	endpoint.Name, endpoint.Group, endpoint.Statistics = "", "", false
	data, err := json.Marshal([]any{endpoint, settings.ProbeTarget, settings.CheckInterval})
	if err != nil {
		return endpoint.ID
	}
	return string(data)
}

func (runner *runner) run(ctx context.Context) {
	defer close(runner.done)
	driver := makeDriver(runner)
	defer driver.dispose()
	attempts, state, message := 0, StateConnecting, ""
	for ctx.Err() == nil {
		runner.update(func(status *Status) {
			status.State, status.Message = state, message
		})
		err := driver.start(ctx)
		if err == nil {
			runner.update(func(status *Status) {
				driver.describe(status)
				status.State, status.Message = StateUp, ""
			})
			err = runner.watch(ctx, driver, func() {
				attempts = 0
			})
		}
		driver.stop()
		if ctx.Err() != nil {
			break
		}
		fatal := &fatalError{}
		runner.logf("link failed: %v", err)
		if state, message = StateReconnecting, err.Error(); !runner.endpoint.IsTunnel() {
			state = StateDown
			runner.fail(state, message)
			sleep(ctx, time.Duration(runner.settings.CheckInterval)*time.Second)
		} else if errors.As(err, &fatal) ||
			attempts >= runner.endpoint.Monitoring.ReconnectAttempts {
			runner.fail(StateFailed, message)
			<-ctx.Done()
		} else {
			attempts++
			message = fmt.Sprintf("reconnect attempt %v of %v: %v", attempts,
				runner.endpoint.Monitoring.ReconnectAttempts, err)
			runner.fail(state, message)
			sleep(ctx, time.Duration(runner.endpoint.Monitoring.ReconnectDelay)*time.Second)
		}
	}
}

func (runner *runner) watch(ctx context.Context, driver driver, healthy func()) error {
	monitoring := runner.endpoint.Monitoring
	active := monitoring != nil && monitoring.Enabled
	interval, timeout, retries := time.Duration(runner.settings.CheckInterval)*time.Second,
		defaultCheckTimeout, 1
	if active {
		interval, timeout = time.Duration(monitoring.Interval)*time.Second,
			time.Duration(monitoring.Timeout)*time.Second
		retries = monitoring.Retries + 1
	} else {
		healthy()
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	if active {
		timer.Reset(min(interval, monitoringGrace))
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
		}
		runner.update(func(status *Status) {
			status.Checked, status.Latency, status.Message = time.Now().Unix(), -1, ""
			if status.State = StateUp; err == nil {
				healthy()
				status.Latency = int(latency.Milliseconds())
			} else if status.Message = err.Error(); !runner.endpoint.IsTunnel() {
				status.State = StateDown
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
