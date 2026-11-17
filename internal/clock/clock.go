package clock

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"
)

type QueryFunc func(ctx context.Context, server string, mark int) (time.Duration, error)
type KernelFunc func() (bool, error)

type Options struct {
	Servers   []string
	Tolerance time.Duration
	Wait      time.Duration
	Interval  time.Duration
	Mark      int
}

type Status struct {
	State    string `json:"state"`
	Offset   int64  `json:"offset"`
	Applied  int64  `json:"applied"`
	Server   string `json:"server"`
	Synced   bool   `json:"host_synchronized"`
	Checked  int64  `json:"checked"`
	Message  string `json:"message"`
	Servers  int    `json:"servers"`
	Answered int    `json:"answered"`
}

type Clock struct {
	mutex      sync.Mutex
	checking   sync.Mutex
	path       string
	options    Options
	status     Status
	offset     time.Duration
	changed    chan struct{}
	cancel     context.CancelFunc
	done       chan struct{}
	queryFunc  QueryFunc
	kernelFunc KernelFunc
}

type sample struct {
	server string
	offset time.Duration
	index  int
}

type fileState struct {
	Offset int64 `json:"offset"`
}

const (
	StateChecking       = "checking"
	StateHost           = "host"
	StateCorrected      = "corrected"
	StateUnsynchronized = "unsynchronized"
)

const ntpPort = "123"
const ntpEpoch = 2208988800
const ntpPacketSize = 48
const ntpClientHeader = 0x23
const ntpServerMode = 4
const ntpUnsynchronized = 3
const queryTimeout = 3 * time.Second
const retryInterval = 10 * time.Second
const kernelUnsynchronized = 0x40
const kernelTimeError = 5

func New(path string) *Clock {
	return NewWith(path, Query, KernelSynchronized)
}

func NewWith(path string, queryFunc QueryFunc, kernelFunc KernelFunc) *Clock {
	return &Clock{path: path, changed: make(chan struct{}), queryFunc: queryFunc,
		kernelFunc: kernelFunc, status: Status{State: StateChecking,
			Message: "time is being checked"}}
}

func Query(ctx context.Context, server string, mark int) (time.Duration, error) {
	address := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		address = net.JoinHostPort(server, ntpPort)
	}
	dialer := net.Dialer{Control: func(_ string, _ string, connection syscall.RawConn) error {
		var failure error
		if err := connection.Control(func(descriptor uintptr) {
			if mark != 0 {
				failure = syscall.SetsockoptInt(int(descriptor), syscall.SOL_SOCKET,
					syscall.SO_MARK, mark)
			}
		}); err != nil {
			return err
		}
		return failure
	}}
	connection, err := dialer.DialContext(ctx, "udp", address)
	if err != nil {
		return 0, fmt.Errorf("failed to connect to NTP server '%v': %w", server, err)
	}
	defer connection.Close()
	if deadline, found := ctx.Deadline(); found {
		_ = connection.SetDeadline(deadline)
	}
	request := make([]byte, ntpPacketSize)
	request[0] = ntpClientHeader
	sent := time.Now()
	putTimestamp(request[40:], sent)
	if _, err = connection.Write(request); err != nil {
		return 0, fmt.Errorf("failed to send request to NTP server '%v': %w", server, err)
	}
	response := make([]byte, ntpPacketSize)
	if count, err := connection.Read(response); err != nil {
		return 0, fmt.Errorf("failed to read response of NTP server '%v': %w", server, err)
	} else if count < ntpPacketSize {
		return 0, fmt.Errorf("unexpected NTP response size (%v)", count)
	}
	received := time.Now()
	if mode := response[0] & 7; mode != ntpServerMode {
		return 0, fmt.Errorf("unexpected NTP mode (%v)", mode)
	} else if response[0]>>6 == ntpUnsynchronized || response[1] == 0 {
		return 0, fmt.Errorf("NTP server '%v' is not synchronized", server)
	} else if !bytes.Equal(response[24:32], request[40:48]) {
		return 0, errors.New("unexpected NTP originate timestamp")
	}
	arrived, departed := readTimestamp(response[32:40]), readTimestamp(response[40:48])
	return (arrived.Sub(sent) + departed.Sub(received)) / 2, nil
}

func KernelSynchronized() (bool, error) {
	timex := syscall.Timex{}
	state, err := syscall.Adjtimex(&timex)
	if err != nil {
		return false, fmt.Errorf("failed to read kernel clock state: %w", err)
	}
	return state != kernelTimeError && timex.Status&kernelUnsynchronized == 0, nil
}

func ReadOffset(path string) (time.Duration, error) {
	state := fileState{}
	if data, err := os.ReadFile(path); err != nil {
		return 0, fmt.Errorf("failed to read file '%v': %w", path, err)
	} else if err = json.Unmarshal(data, &state); err != nil {
		return 0, fmt.Errorf("failed to decode file '%v': %w", path, err)
	}
	return time.Duration(state.Offset) * time.Millisecond, nil
}

func putTimestamp(buffer []byte, moment time.Time) {
	nanoseconds := moment.UnixNano()
	seconds := uint64(nanoseconds/int64(time.Second)) + ntpEpoch
	fraction := uint64(nanoseconds%int64(time.Second)) << 32 / uint64(time.Second)
	binary.BigEndian.PutUint32(buffer[0:4], uint32(seconds))
	binary.BigEndian.PutUint32(buffer[4:8], uint32(fraction))
}

func readTimestamp(buffer []byte) time.Time {
	seconds := int64(binary.BigEndian.Uint32(buffer[0:4])) - ntpEpoch
	fraction := int64(binary.BigEndian.Uint32(buffer[4:8])) * int64(time.Second) >> 32
	return time.Unix(seconds, fraction)
}

func round(offset time.Duration) string {
	if offset.Abs() < time.Second {
		return offset.Round(time.Millisecond).String()
	}
	return offset.Round(100 * time.Millisecond).String()
}

func (clock *Clock) Apply(options Options) {
	if clock == nil {
		return
	}
	clock.mutex.Lock()
	if clock.cancel != nil && slices.Equal(clock.options.Servers, options.Servers) &&
		clock.options.Tolerance == options.Tolerance && clock.options.Wait == options.Wait &&
		clock.options.Interval == options.Interval && clock.options.Mark == options.Mark {
		clock.mutex.Unlock()
		return
	}
	clock.mutex.Unlock()
	clock.Close()
	ctx, cancel := context.WithCancel(context.Background())
	clock.mutex.Lock()
	clock.options, clock.cancel, clock.done = options, cancel, make(chan struct{})
	done := clock.done
	clock.mutex.Unlock()
	go clock.loop(ctx, done)
}

func (clock *Clock) Check(ctx context.Context) Status {
	if clock == nil {
		return Status{State: StateHost}
	}
	clock.mutex.Lock()
	options := clock.options
	clock.mutex.Unlock()
	clock.check(ctx, options)
	return clock.Status()
}

func (clock *Clock) Close() {
	if clock == nil {
		return
	}
	clock.mutex.Lock()
	cancel, done := clock.cancel, clock.done
	clock.cancel, clock.done = nil, nil
	clock.mutex.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (clock *Clock) Now() time.Time {
	if clock == nil {
		return time.Now()
	}
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return time.Now().Add(clock.offset)
}

func (clock *Clock) Offset() time.Duration {
	if clock == nil {
		return 0
	}
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.offset
}

func (clock *Clock) Path() string {
	if clock == nil {
		return ""
	}
	return clock.path
}

func (clock *Clock) Status() Status {
	if clock == nil {
		return Status{State: StateHost}
	}
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.status
}

func (clock *Clock) Wait(ctx context.Context) (Status, error) {
	if clock == nil {
		return Status{State: StateHost}, nil
	}
	clock.mutex.Lock()
	wait := clock.options.Wait
	clock.mutex.Unlock()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		clock.mutex.Lock()
		status, changed := clock.status, clock.changed
		clock.mutex.Unlock()
		if status.Synchronized() {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-changed:
		case <-timer.C:
			return status, fmt.Errorf("time is not synchronized after %v: %v", wait,
				status.Message)
		}
	}
}

func (clock *Clock) WaitLimit() time.Duration {
	if clock == nil {
		return 0
	}
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.options.Wait
}

func (status Status) Synchronized() bool {
	return status.State == StateHost || status.State == StateCorrected
}

func (clock *Clock) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		clock.mutex.Lock()
		options := clock.options
		clock.mutex.Unlock()
		clock.check(ctx, options)
		delay := options.Interval
		if !clock.Status().Synchronized() {
			delay = min(delay, retryInterval)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (clock *Clock) check(ctx context.Context, options Options) {
	clock.checking.Lock()
	defer clock.checking.Unlock()
	synchronized, err := clock.kernelFunc()
	samples := clock.measure(ctx, options)
	if ctx.Err() != nil {
		return
	}
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	status := Status{Synced: synchronized && err == nil, Checked: time.Now().Unix(),
		Servers: len(options.Servers), Answered: len(samples)}
	offset := time.Duration(0)
	if len(samples) != 0 {
		slices.SortFunc(samples, func(left sample, right sample) int {
			if left.offset != right.offset {
				return int(left.offset - right.offset)
			}
			return left.index - right.index
		})
		median := samples[len(samples)/2]
		status.Server, status.Offset = median.server, median.offset.Milliseconds()
		if median.offset.Abs() <= options.Tolerance {
			status.State = StateHost
			status.Message = fmt.Sprintf("host clock is accurate: %v from '%v'",
				round(median.offset), median.server)
		} else {
			status.State, offset = StateCorrected, median.offset
			status.Message = fmt.Sprintf("host clock is %v off '%v', Proximity uses its own time",
				round(median.offset), median.server)
		}
	} else if status.Synced {
		status.State = StateHost
		status.Message = "NTP servers are unreachable, host clock is synchronized"
	} else if clock.status.State == StateCorrected {
		status.State, offset = StateCorrected, clock.offset
		status.Offset, status.Server = clock.status.Offset, clock.status.Server
		status.Message = fmt.Sprintf("NTP servers are unreachable, the last offset %v is kept",
			round(offset))
	} else {
		status.State = StateUnsynchronized
		status.Message = "NTP servers are unreachable and host clock is not synchronized"
	}
	status.Applied = offset.Milliseconds()
	clock.status, clock.offset = status, offset
	clock.save()
	close(clock.changed)
	clock.changed = make(chan struct{})
}

func (clock *Clock) measure(ctx context.Context, options Options) []sample {
	result, mutex, group := []sample{}, sync.Mutex{}, sync.WaitGroup{}
	for index, server := range options.Servers {
		group.Add(1)
		go func() {
			defer group.Done()
			queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
			defer cancel()
			if offset, err := clock.queryFunc(queryCtx, server, options.Mark); err == nil &&
				offset.Abs() < time.Duration(math.MaxInt32)*time.Second {
				mutex.Lock()
				result = append(result, sample{server, offset, index})
				mutex.Unlock()
			}
		}()
	}
	group.Wait()
	return result
}

func (clock *Clock) save() {
	if clock.path == "" {
		return
	} else if data, err := json.Marshal(fileState{Offset: clock.offset.Milliseconds()}); err == nil {
		temporary := clock.path + ".tmp"
		if err = os.WriteFile(temporary, data, 0o644); err == nil {
			_ = os.Rename(temporary, clock.path)
		}
	}
}
