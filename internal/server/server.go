package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proximity/internal/auth"
	"proximity/internal/clock"
	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/router"
)

type Options struct {
	Version      string
	Listen       string
	ConfigPath   string
	RunPath      string
	GeodataPath  string
	Executable   string
	MihomoBinary string
	HostNetwork  bool
	ProxyBind    string
	Tools        link.Tools
	Clock        *clock.Clock
	Logf         func(format string, arguments ...any)
}

type Server struct {
	options   Options
	store     *config.Store
	manager   *link.Manager
	clock     *clock.Clock
	mihomo    *router.Mihomo
	tracker   *router.Tracker
	dirty     chan struct{}
	started   time.Time
	mutex     sync.Mutex
	warnings  []string
	failure   string
	captured  bool
	running   bool
	restart   bool
	requested int64
	served    int64
	statePath string
	mux       *http.ServeMux
	traced    map[string]tracedConnection
}

type tracedConnection struct {
	outbound string
	inbound  string
	text     string
	started  time.Time
	upload   int64
	download int64
}

type traceLines struct {
	outbounds map[string][]string
	inbounds  map[string][]string
}

type routerState struct {
	Running bool `json:"running"`
}

type session struct {
	user  config.User
	level int
}

type statusError struct {
	code    int
	message string
}

type action func(request *http.Request, session session) (any, error)

const cookieName = "proximity_session"
const sessionLifetime = 7 * 24 * time.Hour
const maxBodySize = 4 << 20
const pollInterval = 500 * time.Millisecond
const measureInterval = 2 * time.Second
const saveInterval = 30 * time.Second
const failedLoginDelay = 700 * time.Millisecond
const controlTimeout = 30 * time.Second
const controlPoll = 50 * time.Millisecond
const routerStateName = "router.json"

var geodataFiles = []string{"geoip.metadb", "geosite.dat", "geoip.dat"}

func New(options Options) (*Server, error) {
	result, err := internalNew(options)
	if err != nil {
		return nil, fmt.Errorf("server.New failed: %w", err)
	}
	return result, nil
}

func internalNew(options Options) (*Server, error) {
	store, err := config.Open(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	if message, err := auth.Bootstrap(store); err != nil {
		return nil, err
	} else if message != "" {
		options.Logf("%v", message)
	}
	server := &Server{options: options, store: store, dirty: make(chan struct{}, 1),
		started: time.Now(), warnings: []string{}, mux: http.NewServeMux(),
		traced: map[string]tracedConnection{}, clock: options.Clock}
	if server.clock == nil {
		server.clock = clock.New(filepath.Join(options.RunPath, "clock.json"))
	}
	home := filepath.Join(options.ConfigPath, "mihomo")
	server.tracker = router.NewTracker(filepath.Join(options.ConfigPath, "state.json"))
	if server.manager, err = link.NewManager(link.Options{Directory: options.RunPath,
		StateDir: options.ConfigPath, Executable: options.Executable, Tools: options.Tools,
		HostNetwork: options.HostNetwork, Changed: server.notify, Clock: server.clock}); err != nil {
		return nil, err
	} else if server.mihomo, err = router.NewMihomo(options.MihomoBinary, home,
		filepath.Join(options.RunPath, "mihomo.sock"), server.print); err != nil {
		return nil, err
	}
	for _, name := range geodataFiles {
		source, target := filepath.Join(options.GeodataPath, name), filepath.Join(home, name)
		if _, err = os.Stat(target); err == nil || options.GeodataPath == "" {
			continue
		} else if data, err := os.ReadFile(source); err == nil {
			if err = config.WriteFile(target, data); err != nil {
				return nil, err
			}
		}
	}
	server.statePath = filepath.Join(options.ConfigPath, routerStateName)
	running, err := readRouterState(server.statePath)
	if err != nil {
		options.Logf("%v", err)
	}
	autostart := store.Snapshot().Settings.Autostart
	if !autostart {
		options.Logf("router is stopped: autostart is disabled")
	} else if !running {
		options.Logf("router is stopped: it was stopped before the restart")
	}
	if _, err = server.control(running && autostart, false); err != nil {
		return nil, err
	}
	store.Subscribe(server.notify)
	server.register()
	return server, nil
}

func traceSize(value int64) string {
	units, size, index := []string{"B", "KB", "MB", "GB", "TB"}, float64(value), 0
	for size >= 1024 && index < 4 {
		size, index = size/1024, index+1
	}
	if index == 0 {
		return fmt.Sprintf("%v B", value)
	}
	return fmt.Sprintf("%.1f %v", size, units[index])
}

func readRouterState(path string) (bool, error) {
	state := routerState{Running: true}
	if data, err := os.ReadFile(path); errors.Is(err, os.ErrNotExist) {
		return true, nil
	} else if err != nil {
		return true, fmt.Errorf("failed to read file '%v': %w", path, err)
	} else if err = json.Unmarshal(data, &state); err != nil {
		return true, fmt.Errorf("failed to decode file '%v': %w", path, err)
	}
	return state.Running, nil
}

func fail(code int, format string, arguments ...any) error {
	return &statusError{code: code, message: fmt.Sprintf(format, arguments...)}
}

func decode(request *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, request.Body, maxBodySize))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("failed to decode request: %w", err)
	}
	return nil
}

func (failure *statusError) Error() string {
	return failure.message
}

func (server *Server) Run(ctx context.Context) error {
	listener := &http.Server{Addr: server.options.Listen, Handler: server.mux,
		ReadHeaderTimeout: 10 * time.Second}
	failures, finished := make(chan error, 1), make(chan struct{})
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		failures <- listener.ListenAndServe()
	}()
	go func() {
		server.loop(ctx)
		close(finished)
	}()
	go server.mihomo.Watch(ctx, server.tracker.Observe)
	server.notify()
	var failure error
	select {
	case <-ctx.Done():
	case failure = <-failures:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = listener.Shutdown(shutdown)
	stop()
	<-finished
	server.manager.Close()
	server.clock.Close()
	server.mihomo.Close()
	if err := server.tracker.Save(); err != nil {
		server.options.Logf("%v", err)
	}
	if failure != nil && !errors.Is(failure, http.ErrServerClosed) {
		return fmt.Errorf("failed to serve '%v': %w", server.options.Listen, failure)
	}
	return nil
}

func (server *Server) print(line string) {
	server.options.Logf("mihomo: %v", line)
}

func (server *Server) notify() {
	select {
	case server.dirty <- struct{}{}:
	default:
	}
}

func (server *Server) loop(ctx context.Context) {
	poll, save := time.NewTicker(pollInterval), time.NewTicker(saveInterval)
	defer poll.Stop()
	defer save.Stop()
	measured := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-server.dirty:
			server.synchronize()
		case <-poll.C:
			server.capture()
			connections, err := server.mihomo.Connections()
			if err == nil {
				server.tracker.Update(connections, server.manager.Counters())
				server.trace(connections)
			}
			if err == nil && time.Since(measured) >= measureInterval {
				server.tracker.UpdateInbounds(connections, server.manager.InboundCounters())
				measured = time.Now()
			}
		case <-save.C:
			if err := server.tracker.Save(); err != nil {
				server.options.Logf("%v", err)
			}
		}
	}
}

func (server *Server) trace(connections router.Connections) {
	snapshot := server.store.Snapshot()
	outbounds, inbounds, networks := map[string]bool{}, map[string]bool{}, map[string]netip.Prefix{}
	active := len(server.traced) != 0
	for _, outbound := range snapshot.Outbounds {
		outbounds[outbound.ID], active = outbound.Verbose, active || outbound.Verbose
	}
	for _, inbound := range snapshot.Inbounds {
		if inbounds[inbound.ID], active = inbound.Verbose, active || inbound.Verbose; !inbound.Verbose {
			continue
		} else if prefix, found := inbound.Network(); found {
			networks[inbound.ID] = prefix
		}
	}
	if !active {
		return
	}
	lines, current := traceLines{outbounds: map[string][]string{},
		inbounds: map[string][]string{}}, map[string]tracedConnection{}
	for _, connection := range connections.Items {
		item, known := server.traced[connection.ID]
		if !known {
			item = tracedConnection{text: connection.Describe(), started: time.Now()}
			if outbounds[connection.Outbound()] {
				item.outbound = connection.Outbound()
			}
			if inbounds[connection.Metadata.Inbound] {
				item.inbound = connection.Metadata.Inbound
			} else if address, err := netip.ParseAddr(connection.Metadata.Source); err == nil {
				for id, prefix := range networks {
					if prefix.Contains(address) {
						item.inbound = id
					}
				}
			}
			if item.outbound == "" && item.inbound == "" {
				continue
			}
			lines.add(item, "connection "+item.text)
		}
		item.upload, item.download = connection.Upload, connection.Download
		current[connection.ID] = item
	}
	for id, item := range server.traced {
		if _, alive := current[id]; !alive {
			lines.add(item, fmt.Sprintf("closed after %.1f s, sent %v, received %v: %v",
				time.Since(item.started).Seconds(), traceSize(item.upload),
				traceSize(item.download), item.text))
		}
	}
	server.traced = current
	for id, items := range lines.outbounds {
		server.manager.TraceOutbound(id, items)
	}
	for id, items := range lines.inbounds {
		server.manager.TraceInbound(id, items)
	}
}

func (server *Server) control(running bool, restart bool) (int64, error) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if data, err := json.Marshal(routerState{Running: running}); err != nil {
		return 0, fmt.Errorf("failed to encode router state: %w", err)
	} else if err = config.WriteFile(server.statePath, data); err != nil {
		return 0, fmt.Errorf("failed to save router state: %w", err)
	}
	server.running, server.restart = running, restart && running
	server.requested++
	return server.requested, nil
}

func (server *Server) await(sequence int64) bool {
	for deadline := time.Now().Add(controlTimeout); time.Now().Before(deadline); {
		server.mutex.Lock()
		served := server.served
		server.mutex.Unlock()
		if served >= sequence {
			return true
		}
		time.Sleep(controlPoll)
	}
	return false
}

func (server *Server) complete(sequence int64) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	server.served = max(server.served, sequence)
}

func (server *Server) capture() {
	settings := server.store.Snapshot().Settings
	captured := server.manager.Capture(settings.Capture && server.active(), settings.IPv6)
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if server.captured != captured {
		server.options.Logf("host traffic capture is active: %v", captured)
	}
	server.captured = captured
}

func (server *Server) synchronize() {
	server.mutex.Lock()
	running, restart, sequence := server.running, server.restart, server.requested
	server.restart = false
	server.mutex.Unlock()
	defer server.complete(sequence)
	if !running || restart {
		server.mihomo.Stop()
	}
	snapshot := server.store.Snapshot()
	if !server.options.HostNetwork || !snapshot.Settings.Capture || !running {
		server.capture()
	}
	server.clock.Apply(server.clockOptions(snapshot.Settings))
	server.manager.SetActive(running, snapshot)
	server.manager.Apply(snapshot.Outbounds, snapshot.Settings)
	links := server.manager.Status()
	for _, outbound := range snapshot.Outbounds {
		status, found := links[outbound.ID]
		if !found || !outbound.IsTunnel() || status.State != link.StateUp {
			continue
		}
		if changed, err := snapshot.SetRoutes(outbound.ID, status.Routes); err != nil || !changed {
			continue
		} else if err = server.store.Update(func(config *config.Config) error {
			_, err := config.SetRoutes(outbound.ID, status.Routes)
			return err
		}); err != nil {
			server.options.Logf("failed to save routes of '%v': %v", outbound.ID, err)
		} else {
			server.options.Logf("routes of '%v' changed (%v)", outbound.ID, len(status.Routes))
		}
	}
	snapshot = server.store.Snapshot()
	server.manager.ApplyInbounds(snapshot)
	server.tracker.SetCollected(snapshot.Outbounds)
	server.tracker.SetInbounds(snapshot.Inbounds)
	server.tracker.SetLimits(snapshot.Settings.UnreachableLimit,
		time.Duration(snapshot.Settings.UnreachableLifetime)*time.Second)
	generated, serving := snapshot.Clone(), server.manager.Serving()
	for index, inbound := range generated.Inbounds {
		if !serving[inbound.ID] && generated.InboundProblem(inbound) == "" {
			generated.Inbounds[index].Enabled = false
		}
	}
	data, warnings, err := router.Generate(generated, links, router.Options{
		Socket: server.mihomo.Socket(), ProxyBind: server.options.ProxyBind,
		Capture: server.options.HostNetwork && snapshot.Settings.Capture})
	if err == nil && running {
		err = server.mihomo.Apply(data)
	}
	server.capture()
	server.mutex.Lock()
	defer server.mutex.Unlock()
	if server.warnings, server.failure = warnings, ""; err != nil {
		server.failure = err.Error()
		server.options.Logf("failed to apply router configuration: %v", err)
	}
}

func (server *Server) identify(request *http.Request) session {
	cookie, err := request.Cookie(cookieName)
	if err != nil {
		return session{}
	}
	snapshot := server.store.Snapshot()
	user, err := auth.ParseToken(snapshot.Secret, cookie.Value, snapshot, time.Now())
	if err != nil {
		return session{}
	}
	return session{user: user, level: auth.Level(user.Role)}
}

func (server *Server) handle(pattern string, level int, action action) {
	server.mux.HandleFunc(pattern, func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		current := server.identify(request)
		var result any
		var err error
		if request.Method != http.MethodGet && request.Header.Get("X-Requested-With") == "" {
			err = fail(http.StatusForbidden, "request header does not exist")
		} else if current.level < level && current.level == 0 {
			err = fail(http.StatusUnauthorized, "authentication required")
		} else if current.level < level {
			err = fail(http.StatusForbidden, "access denied for role '%v'", current.user.Role)
		} else {
			result, err = action(request, current)
		}
		code, failure := http.StatusOK, &statusError{}
		if errors.As(err, &failure) {
			code, result = failure.code, map[string]string{"error": failure.message}
		} else if err != nil {
			code, result = http.StatusBadRequest, map[string]string{"error": err.Error()}
		} else if cookie, found := result.(*http.Cookie); found {
			http.SetCookie(writer, cookie)
			result = map[string]bool{"ok": true}
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Proximity-Time", fmt.Sprintf("%.2f",
			float64(time.Since(started).Microseconds())/1000))
		writer.WriteHeader(code)
		if err = json.NewEncoder(writer).Encode(result); err != nil {
			server.options.Logf("failed to write response '%v': %v", pattern, err)
		}
	})
}

func (server *Server) static(dist fs.FS) http.Handler {
	content, err := fs.Sub(dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(content)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if strings.HasPrefix(path, "api/") {
			http.NotFound(writer, request)
			return
		} else if _, err := fs.Stat(content, "index.html"); err != nil {
			http.Error(writer, "web UI is not built: run 'make web' before 'make build'",
				http.StatusServiceUnavailable)
			return
		} else if _, err := fs.Stat(content, path); err != nil || path == "" {
			request.URL.Path = "/"
			writer.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(path, "assets/") {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(writer, request)
	})
}

func (lines traceLines) add(item tracedConnection, line string) {
	if item.outbound != "" {
		lines.outbounds[item.outbound] = append(lines.outbounds[item.outbound], line)
	}
	if item.inbound != "" {
		lines.inbounds[item.inbound] = append(lines.inbounds[item.inbound], line)
	}
}

func (server *Server) clockOptions(settings config.Settings) clock.Options {
	servers, tolerance, interval := settings.TimeSync()
	result := clock.Options{Servers: servers, Tolerance: time.Duration(tolerance) * time.Second,
		Interval: time.Duration(interval) * time.Second,
		Wait:     time.Duration(settings.TimeWait) * time.Second}
	if server.options.HostNetwork && settings.Capture {
		result.Mark = link.CaptureMark
	}
	return result
}
