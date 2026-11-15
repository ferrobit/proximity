package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
)

type fakeDocument struct {
	Controller string `json:"external-controller-unix"`
	LogLevel   string `json:"log-level"`
}

type lineCollector struct {
	mutex sync.Mutex
	lines []string
}

const fakeVariable = "PROXIMITY_FAKE_MIHOMO"

func TestMain(m *testing.M) {
	if os.Getenv(fakeVariable) != "" {
		os.Exit(runFake(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func readFake(path string) (fakeDocument, error) {
	result := fakeDocument{}
	data, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("failed to read file '%v': %w", path, err)
	} else if err = json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("failed to decode file '%v': %w", path, err)
	} else if result.LogLevel == "broken" {
		return result, fmt.Errorf("unexpected log level '%v'", result.LogLevel)
	}
	return result, nil
}

func runFake(arguments []string) int {
	path := arguments[len(arguments)-1]
	document, err := readFake(path)
	if err != nil {
		fmt.Println("level=error msg=\"invalid configuration\"")
		return 1
	} else if slices.Contains(arguments, "-t") {
		return 0
	}
	listener, err := net.Listen("unix", document.Controller)
	if err != nil {
		return 2
	}
	fmt.Println("fake router started")
	routes := http.NewServeMux()
	routes.HandleFunc("GET /version", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"version":"v0.0.1-fake"}`))
	})
	routes.HandleFunc("PUT /configs", func(writer http.ResponseWriter, request *http.Request) {
		body := map[string]string{}
		_ = json.NewDecoder(request.Body).Decode(&body)
		if document, err := readFake(body["path"]); err != nil || document.LogLevel == "rejected" {
			http.Error(writer, `{"message":"rejected"}`, http.StatusBadRequest)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	routes.HandleFunc("GET /connections", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"downloadTotal":300,"uploadTotal":200,"connections":[` +
			`{"id":"c1","upload":5,"download":7,"chains":["A","G"]}]}`))
	})
	routes.HandleFunc("GET /logs", func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"type":"info","payload":"first"}` + "\n" +
			`{"type":"warning","payload":"second"}` + "\n"))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	})
	routes.HandleFunc("POST /exit", func(_ http.ResponseWriter, _ *http.Request) {
		os.Exit(3)
	})
	_ = http.Serve(listener, routes)
	return 0
}

func makeDocument(t *testing.T, socket string, level string, port int) []byte {
	snapshot := config.Default()
	snapshot.Settings.LogLevel, snapshot.Settings.MixedPort = level, port
	data, _, err := Generate(snapshot, nil, Options{Socket: socket})
	require.NoError(t, err)
	return data
}

func (collector *lineCollector) add(line string) {
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	collector.lines = append(collector.lines, line)
}

func (collector *lineCollector) text() string {
	collector.mutex.Lock()
	defer collector.mutex.Unlock()
	return strings.Join(collector.lines, "\n")
}

func TestApply_Lifecycle(t *testing.T) {
	t.Setenv(fakeVariable, "1")
	directory := t.TempDir()
	socket, output := filepath.Join(directory, "mihomo.sock"), &lineCollector{}
	mihomo, err := NewMihomo(os.Args[0], filepath.Join(directory, "home"), socket, output.add)
	require.NoError(t, err)
	defer mihomo.Close()
	require.Equal(t, socket, mihomo.Socket())
	require.False(t, mihomo.State().Running)
	_, err = mihomo.Connections()
	require.ErrorContains(t, err, "failed to get router connections")
	data := makeDocument(t, socket, "warning", 7899)
	require.NoError(t, mihomo.Apply(data))
	state := mihomo.State()
	require.True(t, state.Running)
	require.Equal(t, "v0.0.1-fake", state.Version)
	require.NotZero(t, state.Applied)
	require.Empty(t, state.Message)
	require.Equal(t, data, mihomo.Current())
	require.FileExists(t, filepath.Join(directory, "home", configName))
	require.Eventually(t, func() bool {
		return strings.Contains(output.text(), "fake router started")
	}, 5*time.Second, 20*time.Millisecond)
	{
		require.NoError(t, mihomo.Apply(data))
		changed := makeDocument(t, socket, "warning", 7891)
		require.NoError(t, mihomo.Apply(changed))
		require.Equal(t, changed, mihomo.Current())
	}
	{
		err := mihomo.Apply(makeDocument(t, socket, "rejected", 7899))
		require.ErrorContains(t, err, "failed to reload router configuration")
		require.ErrorContains(t, err, "unexpected response status (400)")
		require.Contains(t, mihomo.State().Message, "failed to reload router configuration")
		err = mihomo.Apply(makeDocument(t, socket, "broken", 7899))
		require.ErrorContains(t, err, "failed to check router configuration")
		require.ErrorContains(t, err, "invalid configuration")
	}
	{
		connections, err := mihomo.Connections()
		require.NoError(t, err)
		require.Equal(t, Connections{Items: []Connection{{ID: "c1", Upload: 5, Download: 7,
			Chains: []string{"A", "G"}}}, Upload: 200, Download: 300}, connections)
	}
	{
		ctx, cancel := context.WithCancel(context.Background())
		payloads, finished := &lineCollector{}, make(chan struct{})
		go func() {
			mihomo.Watch(ctx, payloads.add)
			close(finished)
		}()
		require.Eventually(t, func() bool {
			return payloads.text() == "first\nsecond"
		}, 5*time.Second, 20*time.Millisecond)
		cancel()
		<-finished
	}
	{
		mihomo.Close()
		require.Eventually(t, func() bool {
			return !mihomo.State().Running
		}, 5*time.Second, 20*time.Millisecond)
		require.EqualError(t, mihomo.Apply(data), "router is closed")
	}
}

func TestApply_Restart(t *testing.T) {
	t.Setenv(fakeVariable, "1")
	directory := t.TempDir()
	socket := filepath.Join(directory, "mihomo.sock")
	mihomo, err := NewMihomo(os.Args[0], directory, socket, func(string) {})
	require.NoError(t, err)
	defer mihomo.Close()
	require.NoError(t, mihomo.Apply(makeDocument(t, socket, "warning", 7899)))
	require.Error(t, mihomo.request(http.MethodPost, "/exit", nil, nil))
	require.Eventually(t, func() bool {
		state := mihomo.State()
		return !state.Running && state.Restarts == 1 && strings.Contains(state.Message,
			"router exited")
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		state := mihomo.State()
		return state.Running && state.Message == ""
	}, 10*time.Second, 50*time.Millisecond)
	connections, err := mihomo.Connections()
	require.NoError(t, err)
	require.Len(t, connections.Items, 1)
}

func TestNewMihomo_Problems(t *testing.T) {
	directory := t.TempDir()
	blocker := filepath.Join(directory, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	{
		_, err := NewMihomo("mihomo", filepath.Join(blocker, "home"), "socket", nil)
		require.ErrorContains(t, err, "failed to create directory")
	}
	{
		mihomo, err := NewMihomo(filepath.Join(directory, "missing"), directory, "socket", nil)
		require.NoError(t, err)
		err = mihomo.Apply([]byte("{}"))
		require.ErrorContains(t, err, "failed to check router configuration")
		require.Contains(t, mihomo.State().Message, "failed to check router configuration")
		mihomo.Close()
	}
}
