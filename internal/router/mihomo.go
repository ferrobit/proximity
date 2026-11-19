package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"proximity/internal/config"
)

type Metadata struct {
	Inbound     string `json:"inboundName"`
	Network     string `json:"network"`
	Source      string `json:"sourceIP"`
	SourcePort  string `json:"sourcePort"`
	Destination string `json:"destinationIP"`
	Port        string `json:"destinationPort"`
	Host        string `json:"host"`
	Process     string `json:"process"`
}

type Connection struct {
	ID       string   `json:"id"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Chains   []string `json:"chains"`
	Rule     string   `json:"rule"`
	Payload  string   `json:"rulePayload"`
	Metadata Metadata `json:"metadata"`
}

type RuleExtra struct {
	HitCount int64     `json:"hitCount"`
	HitAt    time.Time `json:"hitAt"`
}

type Rule struct {
	Index   int       `json:"index"`
	Type    string    `json:"type"`
	Payload string    `json:"payload"`
	Proxy   string    `json:"proxy"`
	Extra   RuleExtra `json:"extra"`
}

type ruleList struct {
	Rules []Rule `json:"rules"`
}

type Connections struct {
	Items    []Connection `json:"connections"`
	Upload   int64        `json:"uploadTotal"`
	Download int64        `json:"downloadTotal"`
}

type State struct {
	Running  bool   `json:"running"`
	Stopped  bool   `json:"stopped"`
	Version  string `json:"version"`
	Message  string `json:"message"`
	Restarts int    `json:"restarts"`
	Applied  int64  `json:"applied"`
}

type logEvent struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

type dnsRecord struct {
	Data string `json:"data"`
	Type int    `json:"type"`
}

type dnsReply struct {
	Status  int         `json:"Status"`
	Answer  []dnsRecord `json:"Answer"`
	Message string      `json:"message"`
}

type Mihomo struct {
	mutex     sync.Mutex
	binary    string
	directory string
	socket    string
	output    func(line string)
	client    *http.Client
	streamer  *http.Client
	command   *exec.Cmd
	finished  chan struct{}
	current   []byte
	state     State
	closed    bool
	session   int
}

const configName = "config.yaml"
const logsPath = "/logs?level=info"
const restartDelay = 2 * time.Second
const startTimeout = 15 * time.Second
const dnsPath = "/dns/query?name=%v&type=%v"
const dnsTimeout = 8 * time.Second

var dnsKinds = []struct {
	name string
	code int
}{{"A", 1}, {"AAAA", 28}}

func NewMihomo(binary string, directory string, socket string,
	output func(line string)) (*Mihomo, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create directory '%v': %w", directory, err)
	}
	result := &Mihomo{binary: binary, directory: directory, socket: socket, output: output}
	transport := &http.Transport{DialContext: func(ctx context.Context, _ string,
		_ string) (net.Conn, error) {
		dialer := net.Dialer{}
		return dialer.DialContext(ctx, "unix", result.Socket())
	}}
	result.client = &http.Client{Timeout: 10 * time.Second, Transport: transport}
	result.streamer = &http.Client{Transport: transport}
	return result, nil
}

func (mihomo *Mihomo) Socket() string {
	return mihomo.socket
}

func (mihomo *Mihomo) State() State {
	mihomo.mutex.Lock()
	defer mihomo.mutex.Unlock()
	return mihomo.state
}

func (mihomo *Mihomo) Current() []byte {
	mihomo.mutex.Lock()
	defer mihomo.mutex.Unlock()
	return bytes.Clone(mihomo.current)
}

func (mihomo *Mihomo) Apply(data []byte) error {
	mihomo.mutex.Lock()
	defer mihomo.mutex.Unlock()
	if mihomo.closed {
		return errors.New("router is closed")
	} else if bytes.Equal(data, mihomo.current) && mihomo.command != nil {
		return nil
	}
	mihomo.state.Stopped = false
	err := mihomo.apply(data)
	if mihomo.state.Message = ""; err != nil {
		mihomo.state.Message = err.Error()
	}
	return err
}

func (mihomo *Mihomo) Connections() (Connections, error) {
	result := Connections{}
	if err := mihomo.request(http.MethodGet, "/connections", nil, &result); err != nil {
		return Connections{}, fmt.Errorf("failed to get router connections: %w", err)
	}
	return result, nil
}

// NOTE: The session changes with every reload or start of mihomo, where its counters start
// from zero; rules read across such a change are refused.
func (mihomo *Mihomo) Rules() (int, []Rule, error) {
	session, result := mihomo.currentSession(), ruleList{}
	if err := mihomo.request(http.MethodGet, "/rules", nil, &result); err != nil {
		return 0, nil, fmt.Errorf("failed to get router rules: %w", err)
	} else if mihomo.currentSession() != session {
		return 0, nil, errors.New("router reloaded while reading rules")
	}
	return session, result.Rules, nil
}

// NOTE: The answer comes from the resolver of mihomo, so it follows the DNS servers of the
// routes and is a real address, also while applications get placeholder (fake-IP) answers.
func (mihomo *Mihomo) Resolve(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	var failure error
	for _, kind := range dnsKinds {
		reply, result := dnsReply{}, []string{}
		path := fmt.Sprintf(dnsPath, url.QueryEscape(host), kind.name)
		if err := mihomo.query(ctx, path, &reply); err != nil {
			return nil, fmt.Errorf("failed to resolve '%v': %w", host, err)
		} else if reply.Message != "" {
			return nil, fmt.Errorf("failed to resolve '%v': %v", host, reply.Message)
		} else if reply.Status != 0 {
			failure = fmt.Errorf("failed to resolve '%v': DNS status (%v)", host, reply.Status)
		}
		for _, record := range reply.Answer {
			if record.Type == kind.code {
				result = append(result, record.Data)
			}
		}
		if len(result) != 0 {
			return result, nil
		}
	}
	if failure != nil {
		return nil, failure
	}
	return nil, fmt.Errorf("failed to resolve '%v': address does not exist", host)
}

func (mihomo *Mihomo) Watch(ctx context.Context, observe func(payload string)) {
	for ctx.Err() == nil {
		if err := mihomo.stream(ctx, observe); err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(restartDelay):
			}
		}
	}
}

func (mihomo *Mihomo) Stop() {
	mihomo.mutex.Lock()
	mihomo.state.Stopped, mihomo.state.Message = true, ""
	command, finished := mihomo.command, mihomo.finished
	mihomo.mutex.Unlock()
	terminate(command, finished)
}

func (mihomo *Mihomo) Close() {
	mihomo.mutex.Lock()
	mihomo.closed = true
	command, finished := mihomo.command, mihomo.finished
	mihomo.mutex.Unlock()
	terminate(command, finished)
}

func (mihomo *Mihomo) currentSession() int {
	mihomo.mutex.Lock()
	defer mihomo.mutex.Unlock()
	return mihomo.session
}

func (mihomo *Mihomo) apply(data []byte) error {
	candidate := filepath.Join(mihomo.directory, "candidate.yaml")
	path := filepath.Join(mihomo.directory, configName)
	if err := config.WriteFile(candidate, data); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, mihomo.binary, "-t", "-d", mihomo.directory, "-f",
		candidate).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to check router configuration (%v): %w", lastLine(output), err)
	} else if err = os.Rename(candidate, path); err != nil {
		return fmt.Errorf("failed to rename file '%v': %w", candidate, err)
	}
	mihomo.current, mihomo.state.Applied = bytes.Clone(data), time.Now().Unix()
	mihomo.session++
	if mihomo.command == nil {
		return mihomo.start()
	}
	body := map[string]string{"path": path}
	if err := mihomo.request(http.MethodPut, "/configs?force=true", body, nil); err != nil {
		return fmt.Errorf("failed to reload router configuration: %w", err)
	}
	return nil
}

func (mihomo *Mihomo) start() error {
	_ = os.Remove(mihomo.Socket())
	command := exec.Command(mihomo.binary, "-d", mihomo.directory, "-f",
		filepath.Join(mihomo.directory, configName))
	reader, writer := io.Pipe()
	command.Stdout, command.Stderr = writer, writer
	if err := command.Start(); err != nil {
		return fmt.Errorf("failed to start '%v': %w", mihomo.binary, err)
	}
	finished := make(chan struct{})
	mihomo.command, mihomo.finished, mihomo.state.Running = command, finished, true
	mihomo.session++
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			mihomo.output(scanner.Text())
		}
	}()
	go mihomo.supervise(command, writer, finished)
	deadline := time.Now().Add(startTimeout)
	version := map[string]any{}
	for mihomo.request(http.MethodGet, "/version", nil, &version) != nil {
		if time.Now().After(deadline) {
			return errors.New("router API does not respond")
		}
		time.Sleep(100 * time.Millisecond)
	}
	mihomo.state.Version = fmt.Sprint(version["version"])
	return nil
}

func (mihomo *Mihomo) supervise(command *exec.Cmd, writer *io.PipeWriter, finished chan struct{}) {
	err := command.Wait()
	_ = writer.Close()
	mihomo.mutex.Lock()
	mihomo.command, mihomo.state.Running = nil, false
	closed := mihomo.closed || mihomo.state.Stopped
	if !closed {
		mihomo.state.Restarts++
		mihomo.state.Message = fmt.Sprintf("router exited: %v", err)
	}
	mihomo.mutex.Unlock()
	close(finished)
	for !closed {
		time.Sleep(restartDelay)
		mihomo.mutex.Lock()
		if closed = mihomo.closed || mihomo.state.Stopped || mihomo.command != nil; !closed {
			if err = mihomo.start(); err == nil {
				mihomo.state.Message, closed = "", true
			} else {
				mihomo.state.Message = err.Error()
			}
		}
		mihomo.mutex.Unlock()
	}
}

func (mihomo *Mihomo) stream(ctx context.Context, observe func(payload string)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mihomo"+logsPath, nil)
	if err != nil {
		return fmt.Errorf("failed to create request '%v': %w", logsPath, err)
	}
	response, err := mihomo.streamer.Do(request)
	if err != nil {
		return fmt.Errorf("failed to send request '%v': %w", logsPath, err)
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(response.Body)
	for {
		event := logEvent{}
		if err = decoder.Decode(&event); err != nil {
			return fmt.Errorf("failed to decode response '%v': %w", logsPath, err)
		}
		observe(event.Payload)
	}
}

func (mihomo *Mihomo) query(ctx context.Context, path string, result any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mihomo"+path, nil)
	if err != nil {
		return fmt.Errorf("failed to create request '%v': %w", path, err)
	}
	response, err := mihomo.client.Do(request)
	if err != nil {
		return fmt.Errorf("failed to send request '%v': %w", path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("failed to read response '%v': %w", path, err)
	} else if err = json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("failed to decode response '%v' (%v): %w", path, response.StatusCode, err)
	}
	return nil
}

func (mihomo *Mihomo) request(method string, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, "http://mihomo"+path, reader)
	if err != nil {
		return fmt.Errorf("failed to create request '%v': %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := mihomo.client.Do(request)
	if err != nil {
		return fmt.Errorf("failed to send request '%v': %w", path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("failed to read response '%v': %w", path, err)
	} else if response.StatusCode >= 300 {
		return fmt.Errorf("unexpected response status (%v) '%v'", response.StatusCode,
			strings.TrimSpace(string(data)))
	} else if result == nil || len(data) == 0 {
		return nil
	} else if err = json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("failed to decode response '%v': %w", path, err)
	}
	return nil
}

func terminate(command *exec.Cmd, finished chan struct{}) {
	if command == nil {
		return
	}
	_ = command.Process.Signal(syscall.SIGTERM)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-finished
	}
}

func lastLine(output []byte) string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	return lines[len(lines)-1]
}
