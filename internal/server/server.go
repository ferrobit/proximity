package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"proximity/internal/auth"
	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/router"
	"proximity/web"
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
	Logf         func(format string, arguments ...any)
}

type Server struct {
	options   Options
	store     *config.Store
	manager   *link.Manager
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
		started: time.Now(), warnings: []string{}, mux: http.NewServeMux()}
	home := filepath.Join(options.ConfigPath, "mihomo")
	server.tracker = router.NewTracker(filepath.Join(options.ConfigPath, "state.json"))
	if server.manager, err = link.NewManager(link.Options{Directory: options.RunPath,
		StateDir: options.ConfigPath, Executable: options.Executable, Tools: options.Tools,
		HostNetwork: options.HostNetwork, Changed: server.notify}); err != nil {
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

func (server *Server) static() http.Handler {
	content, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(content)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if strings.HasPrefix(path, "api/") {
			http.NotFound(writer, request)
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
