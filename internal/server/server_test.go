package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/web"
)

const sampleKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
const sampleTunnel = "[Interface]\nPrivateKey = " + sampleKey + "\nAddress = 10.8.1.2/32\n" +
	"[Peer]\nPublicKey = " + sampleKey + "\nEndpoint = vpn.example.com:51820\n"
const sampleOpenVPN = "client\ndev tun\nremote vpn.example.com 1194 udp\n"

func makeServer(t *testing.T) *Server {
	directory := t.TempDir()
	tool := filepath.Join(directory, "ip")
	require.NoError(t, os.WriteFile(tool, []byte("#!/bin/bash\nexit 1\n"), 0o700))
	geodata := filepath.Join(directory, "geodata")
	require.NoError(t, os.MkdirAll(geodata, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(geodata, "geosite.dat"), []byte("geo"), 0o600))
	t.Setenv("PROXIMITY_ADMIN_USER", "admin")
	t.Setenv("PROXIMITY_ADMIN_PASSWORD", "secret123")
	server, err := New(Options{Version: "test", Listen: "127.0.0.1:0",
		ConfigPath: filepath.Join(directory, "config"), RunPath: filepath.Join(directory, "run"),
		GeodataPath: geodata, Executable: "/usr/local/bin/proximity", MihomoBinary: "/bin/false",
		Tools: link.Tools{IP: tool}, Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(server.manager.Close)
	require.FileExists(t, filepath.Join(directory, "config", "mihomo", "geosite.dat"))
	return server
}

func call(t *testing.T, server *Server, cookie *http.Cookie, method string, path string,
	body any) (int, map[string]any, *http.Cookie) {
	buffer := bytes.Buffer{}
	if body != nil {
		require.NoError(t, json.NewEncoder(&buffer).Encode(body))
	}
	request := httptest.NewRequest(method, path, &buffer)
	request.Header.Set("X-Requested-With", "test")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.routes.ServeHTTP(recorder, request)
	result, raw := map[string]any{}, json.RawMessage{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &raw), recorder.Body.String())
	if json.Unmarshal(raw, &result) != nil {
		list := []any{}
		require.NoError(t, json.Unmarshal(raw, &list))
		result["list"] = list
	}
	for _, item := range recorder.Result().Cookies() {
		if item.Name == cookieName {
			return recorder.Code, result, item
		}
	}
	return recorder.Code, result, nil
}

func fetch(server *Server, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	server.routes.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	return recorder
}

func login(t *testing.T, server *Server, name string, password string) *http.Cookie {
	code, _, cookie := call(t, server, nil, http.MethodPost, "/api/login",
		loginRequest{Username: name, Password: password})
	require.Equal(t, http.StatusOK, code)
	require.NotNil(t, cookie)
	return cookie
}

func TestNew_Problems(t *testing.T) {
	directory := t.TempDir()
	blocker := filepath.Join(directory, "file")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	_, err := New(Options{ConfigPath: filepath.Join(blocker, "config"), Logf: t.Logf})
	require.ErrorContains(t, err, "server.New failed: config.Open failed")
	{
		t.Setenv("PROXIMITY_ADMIN_PASSWORD", "secret123")
		_, err := New(Options{ConfigPath: filepath.Join(directory, "config"),
			RunPath: filepath.Join(directory, "run"), Tools: link.Tools{IP: blocker + "-missing"},
			Logf: t.Logf})
		require.ErrorContains(t, err, "ip tool does not exist")
	}
}

func TestRun_Shutdown(t *testing.T) {
	server := makeServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- server.Run(ctx)
	}()
	require.Eventually(t, func() bool {
		_, failure := server.problems()
		return strings.Contains(failure, "failed to check router configuration")
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	require.NoError(t, <-finished)
	{
		server := makeServer(t)
		server.options.Listen = "256.0.0.1:99999"
		require.ErrorContains(t, server.Run(context.Background()), "failed to serve")
	}
}

func TestServer_Access(t *testing.T) {
	server := makeServer(t)
	code, body, _ := call(t, server, nil, http.MethodGet, "/api/session", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, map[string]any{"user": "", "role": "anonymous", "level": 0.0,
		"version": "test", "anonymous_dashboard": true}, body)
	code, body, _ = call(t, server, nil, http.MethodGet, "/api/endpoints", nil)
	require.Equal(t, http.StatusUnauthorized, code)
	require.Equal(t, "authentication required", body["error"])
	{
		request := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
		recorder := httptest.NewRecorder()
		server.routes.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.Contains(t, recorder.Body.String(), "request header does not exist")
		require.NotEmpty(t, recorder.Header().Get("X-Proximity-Time"))
	}
	{
		code, body, cookie := call(t, server, nil, http.MethodPost, "/api/login",
			loginRequest{Username: "admin", Password: "wrong"})
		require.Equal(t, http.StatusUnauthorized, code)
		require.Equal(t, "invalid user name or password", body["error"])
		require.Nil(t, cookie)
		code, _, _ = call(t, server, nil, http.MethodPost, "/api/login", "text")
		require.Equal(t, http.StatusBadRequest, code)
	}
	admin := login(t, server, "admin", "secret123")
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/session", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "administrator", body["role"])
		require.Equal(t, 3.0, body["level"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123"})
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "user 'viewer' already exists", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "short", Role: config.RoleUser, Password: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/users", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{map[string]any{"name": "admin", "role": "administrator"},
			map[string]any{"name": "viewer", "role": "user"}}, body["list"])
		code, body, _ = call(t, server, admin, http.MethodPut, "/api/users/admin",
			userRequest{Role: config.RoleUser})
		require.Equal(t, "own role cannot be changed", body["error"])
		code, body, _ = call(t, server, admin, http.MethodDelete, "/api/users/admin", nil)
		require.Equal(t, "own account cannot be removed", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
	}
	viewer := login(t, server, "viewer", "viewer123")
	{
		code, body, _ := call(t, server, viewer, http.MethodPost, "/api/endpoints",
			config.Endpoint{Type: config.TypeHost})
		require.Equal(t, http.StatusForbidden, code)
		require.Equal(t, "access denied for role 'user'", body["error"])
		code, _, _ = call(t, server, viewer, http.MethodGet, "/api/users", nil)
		require.Equal(t, http.StatusForbidden, code)
		code, body, _ = call(t, server, viewer, http.MethodPost, "/api/password",
			passwordRequest{Current: "wrong", Password: "viewer456"})
		require.Equal(t, "current password is invalid", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, viewer, http.MethodPost, "/api/password",
			passwordRequest{Current: "viewer123", Password: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		code, _, cleared := call(t, server, viewer, http.MethodPost, "/api/password",
			passwordRequest{Current: "viewer123", Password: "viewer456"})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, -1, cleared.MaxAge)
		code, body, _ = call(t, server, viewer, http.MethodGet, "/api/session", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "anonymous", body["role"])
		viewer = login(t, server, "viewer", "viewer456")
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/users/viewer",
			userRequest{Role: config.RoleOperator, Password: "viewer789"})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/users/viewer",
			userRequest{Password: "1"})
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, viewer, http.MethodGet, "/api/endpoints", nil)
		require.Equal(t, http.StatusUnauthorized, code)
		operator := login(t, server, "viewer", "viewer789")
		code, _, _ = call(t, server, operator, http.MethodPost, "/api/endpoints",
			config.Endpoint{Type: config.TypeHost, Enabled: true})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, operator, http.MethodPut, "/api/settings", config.Settings{})
		require.Equal(t, http.StatusForbidden, code)
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/users/viewer", nil)
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, operator, http.MethodGet, "/api/endpoints", nil)
		require.Equal(t, http.StatusUnauthorized, code)
	}
	{
		settings := server.store.Snapshot().Settings
		settings.AnonymousDashboard = false
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/settings", settings)
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/settings", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, false, body["anonymous_dashboard"])
		code, _, _ = call(t, server, nil, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusUnauthorized, code)
		settings.LogLevel = "loud"
		code, body, _ = call(t, server, admin, http.MethodPut, "/api/settings", settings)
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "unexpected log level 'loud'")
		code, _, cleared := call(t, server, admin, http.MethodPost, "/api/logout", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, -1, cleared.MaxAge)
	}
}

func TestServer_Endpoints(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	for _, endpoint := range []config.Endpoint{
		{Type: config.TypeHost, Enabled: true, Group: "uplinks", Statistics: true},
		{Type: config.TypeSocks5, Enabled: true, Group: "work", Socks5: &config.Socks5Options{
			Server: "127.0.0.1", Port: 1, Username: "user", Password: "pass"}},
		{Type: config.TypeHTTP, Group: "work", HTTP: &config.HTTPOptions{Server: "proxy.test",
			Port: 3128, Password: "pass", Single: true}},
		{Type: config.TypeOpenVPN, OpenVPN: &config.OpenVPNOptions{Config: sampleOpenVPN,
			Password: "pass", OTPKey: "GEZDGNBVGY3TQOJQ", KeyPassword: "key"}},
		{Type: config.TypeAmneziaWG, AmneziaWG: &config.AmneziaWGOptions{Config: sampleTunnel}}} {
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/endpoints", endpoint)
		require.Equal(t, http.StatusOK, code, body)
	}
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/endpoints",
		config.Endpoint{Type: "unknown"})
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "unexpected endpoint type 'unknown'", body["error"])
	code, _, _ = call(t, server, admin, http.MethodPost, "/api/endpoints", "text")
	require.Equal(t, http.StatusBadRequest, code)
	server.synchronize()
	require.Eventually(t, func() bool {
		return server.manager.Status()["SOCKS5 1"].State == link.StateDown
	}, 10*time.Second, 20*time.Millisecond)
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/endpoints", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{"uplinks", "work"}, body["groups"])
		views := body["endpoints"].([]any)
		require.Len(t, views, 5)
		first, second := views[0].(map[string]any), views[1].(map[string]any)
		require.Equal(t, "HOST 1", first["id"])
		require.Equal(t, "unused", first["usage"])
		require.Equal(t, false, first["redacted"])
		require.Equal(t, "pass", second["socks5"].(map[string]any)["password"])
		require.Equal(t, "down", second["status"].(map[string]any)["state"])
		require.Equal(t, "disabled", views[2].(map[string]any)["usage"])
		require.Equal(t, "disabled", views[3].(map[string]any)["status"].(map[string]any)["state"])
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/users",
			userRequest{Name: "viewer", Role: config.RoleUser, Password: "viewer123"})
		require.Equal(t, http.StatusOK, code)
		viewer := login(t, server, "viewer", "viewer123")
		code, body, _ := call(t, server, viewer, http.MethodGet, "/api/endpoints", nil)
		require.Equal(t, http.StatusOK, code)
		views := body["endpoints"].([]any)
		require.Equal(t, true, views[1].(map[string]any)["redacted"])
		require.Equal(t, "", views[1].(map[string]any)["socks5"].(map[string]any)["password"])
		require.Equal(t, "user", views[1].(map[string]any)["socks5"].(map[string]any)["username"])
		require.Equal(t, "", views[2].(map[string]any)["http"].(map[string]any)["password"])
		tunnel := views[3].(map[string]any)["openvpn"].(map[string]any)
		require.Equal(t, "", tunnel["config"])
		require.Equal(t, "", tunnel["otp_key"])
		require.Equal(t, "", views[4].(map[string]any)["amneziawg"].(map[string]any)["config"])
		code, _, _ = call(t, server, viewer, http.MethodGet, "/api/endpoints/HOST%201/log", nil)
		require.Equal(t, http.StatusForbidden, code)
	}
	{
		code, body, _ := call(t, server, nil, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		views := body["endpoints"].([]any)
		second := views[1].(map[string]any)
		require.Equal(t, "SOCKS5 1", second["id"])
		require.NotContains(t, second, "socks5")
		require.Equal(t, "down", second["status"].(map[string]any)["state"])
		require.Equal(t, "", second["status"].(map[string]any)["message"])
		require.Empty(t, body["warnings"])
		require.Equal(t, map[string]any{"endpoints": 5.0, "endpoints_enabled": 2.0,
			"endpoints_up": 1.0, "fallbacks": 0.0, "filters": 0.0, "rules": 0.0}, body["counts"])
		require.Contains(t, body["failure"], "failed to check router configuration")
		require.Equal(t, 7890.0, body["proxy_port"])
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/endpoints/SOCKS5%201",
			config.Endpoint{Name: "renamed", Enabled: true, Socks5: &config.Socks5Options{
				Server: "127.0.0.1", Port: 2}})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/endpoints/SOCKS5%201", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/endpoints/missing",
			config.Endpoint{})
		require.Equal(t, "endpoint 'missing' does not exist", body["error"])
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/endpoints/SOCKS5%201/move",
			moveRequest{Delta: -1})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/endpoints/SOCKS5%201/move", "x")
		require.Equal(t, http.StatusBadRequest, code)
		snapshot := server.store.Snapshot()
		require.Equal(t, "SOCKS5 1", snapshot.Endpoints[0].ID)
		require.Equal(t, "renamed", snapshot.Endpoints[0].Name)
		require.Equal(t, 2, snapshot.Endpoints[0].Socks5.Port)
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/endpoints/HOST%201/restart", nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/endpoints/HTTP%201/restart",
			nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "endpoint 'HTTP 1' is not running", body["error"])
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/endpoints/HOST%201/log", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, body, "lines")
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/interfaces", nil)
		require.Equal(t, http.StatusOK, code)
		require.Contains(t, body, "list")
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/targets", nil)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, body["list"], 7)
		require.Equal(t, map[string]any{"id": "DIRECT", "kind": "builtin", "enabled": true},
			body["list"].([]any)[0])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/import/amneziawg",
			importRequest{Text: sampleTunnel})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, strings.TrimSpace(sampleTunnel)+"\n", body["config"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/import/amneziawg",
			importRequest{Text: "vpn://broken"})
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/import/amneziawg", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/endpoints/HTTP%201", nil)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, server.store.Snapshot().Endpoints, 4)
	}
}

func TestServer_Filters(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	for _, endpoint := range []config.Endpoint{{Type: config.TypeHost, Enabled: true},
		{Type: config.TypeHost, Enabled: true}} {
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/endpoints", endpoint)
		require.Equal(t, http.StatusOK, code)
	}
	code, body, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
		config.Fallback{Enabled: true, Endpoints: []string{"HOST 1"}})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "Fallback 1", body["id"])
	{
		code, _, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
			config.Fallback{ID: "Second", Endpoints: []string{"HOST 2", "HOST 1"}})
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/fallbacks",
			config.Fallback{Endpoints: []string{"missing"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "endpoint 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/fallbacks", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/fallbacks/Second",
			config.Fallback{Enabled: true, Endpoints: []string{"HOST 2"}, Interval: 120})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/fallbacks/Second", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/fallbacks/Second/move",
			moveRequest{Delta: -1})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/fallbacks/Second/move", "x")
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/fallbacks", nil)
		require.Equal(t, http.StatusOK, code)
		list := body["list"].([]any)
		require.Equal(t, "Second", list[0].(map[string]any)["id"])
		require.Equal(t, 120.0, list[0].(map[string]any)["interval"])
	}
	keys := []string{}
	for _, filter := range []config.Filter{
		{ID: "corp", Directive: "DOMAIN-SUFFIX", Target: "Fallback 1", Enabled: true,
			Params: []string{"https://Corp.Test/path", ""}},
		{Directive: "DOMAIN", Target: "HOST 2", Enabled: true, Params: []string{"wiki.corp.test"}},
		{Directive: "IP-CIDR", Target: "REJECT", Enabled: true, Params: []string{"1.2.3.4:443"}}} {
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/filters", filter)
		require.Equal(t, http.StatusOK, code, body)
		keys = append(keys, body["key"].(string))
	}
	{
		code, body, _ := call(t, server, admin, http.MethodPost, "/api/filters",
			config.Filter{Directive: "DOMAIN", Target: "missing", Params: []string{"a.test"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "target 'missing' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/filters", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/filters/preview",
			config.Filter{Directive: "IP-CIDR", Target: "HOST 1",
				Params: []string{"10.1.2.3:8080", "192.168.0.1"}})
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, []any{"AND,((IP-CIDR,10.1.2.3/32),(DST-PORT,8080)),HOST 1",
			"IP-CIDR,192.168.0.1/32,HOST 1"}, body["lines"])
		require.Equal(t, []any{"10.1.2.3/32:8080", "192.168.0.1/32"}, body["params"])
		code, body, _ = call(t, server, admin, http.MethodPost, "/api/filters/preview",
			config.Filter{Directive: "DOMAIN", Params: []string{"bad domain"}})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "failed to parse line 1")
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/filters/preview", 5)
		require.Equal(t, http.StatusBadRequest, code)
	}
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/filters/default",
			targetRequest{Target: "Second"})
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodPut, "/api/filters/default",
			targetRequest{Target: "missing"})
		require.Equal(t, http.StatusBadRequest, code)
		require.Contains(t, body["error"], "default target 'missing' does not exist")
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/filters/default", 5)
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/filters/"+keys[2]+"/move",
			moveRequest{Delta: -1})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPost, "/api/filters/"+keys[2]+"/move", "x")
		require.Equal(t, http.StatusBadRequest, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/filters/"+keys[1],
			config.Filter{Directive: "DOMAIN", Target: "HOST 2", Enabled: true,
				Params: []string{"wiki.corp.test", "corp.test"}})
		require.Equal(t, http.StatusOK, code)
		code, _, _ = call(t, server, admin, http.MethodPut, "/api/filters/"+keys[1], 5)
		require.Equal(t, http.StatusBadRequest, code)
		server.synchronize()
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/filters", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "Second", body["default_target"])
		require.Contains(t, body["directives"], "GEOSITE")
		require.Contains(t, body["failure"], "failed to check router configuration")
		filters := body["filters"].([]any)
		require.Len(t, filters, 3)
		require.Equal(t, keys[2], filters[1].(map[string]any)["key"])
		require.Equal(t, []any{"corp.test"}, filters[0].(map[string]any)["params"])
		warnings := body["warnings"].([]any)
		require.Len(t, warnings, 2)
		require.Contains(t, warnings[0].(map[string]any)["message"],
			"DOMAIN-SUFFIX 'corp.test' in 'corp' covers DOMAIN 'wiki.corp.test' in #3")
	}
	{
		code, body, _ := call(t, server, admin, http.MethodGet, "/api/router/config", nil)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "", body["config"])
		server.tracker.Observe("[TCP] dial HOST 1 (match Match/) 10.0.0.1:1 --> a.test:80 " +
			"error: connect: connection refused")
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		require.Len(t, body["refused"], 1)
		require.Equal(t, 4.0, body["counts"].(map[string]any)["rules"])
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/refused", nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ = call(t, server, admin, http.MethodGet, "/api/dashboard", nil)
		require.Equal(t, http.StatusOK, code)
		require.Empty(t, body["refused"])
	}
	{
		code, _, _ := call(t, server, admin, http.MethodDelete, "/api/filters/"+keys[0], nil)
		require.Equal(t, http.StatusOK, code)
		code, body, _ := call(t, server, admin, http.MethodDelete, "/api/filters/"+keys[0], nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "filter '"+keys[0]+"' does not exist", body["error"])
		code, _, _ = call(t, server, admin, http.MethodDelete, "/api/fallbacks/Second", nil)
		require.Equal(t, http.StatusOK, code)
		snapshot := server.store.Snapshot()
		require.Equal(t, config.TargetDirect, snapshot.Settings.DefaultTarget)
		require.Len(t, snapshot.Filters, 2)
	}
}

func TestServer_Routes(t *testing.T) {
	server := makeServer(t)
	admin := login(t, server, "admin", "secret123")
	code, _, _ := call(t, server, admin, http.MethodPost, "/api/endpoints", config.Endpoint{
		Type: config.TypeAmneziaWG, Enabled: true, AmneziaWG: &config.AmneziaWGOptions{
			Config: sampleTunnel + "AllowedIPs = 10.55.0.0/16\n"}})
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, server.store.Update(func(config *config.Config) error {
		_, err := config.SetRoutes("AmneziaWG 1", []string{"10.55.0.0/16"})
		return err
	}))
	snapshot := server.store.Snapshot()
	require.Len(t, snapshot.Filters, 1)
	require.Equal(t, "AmneziaWG 1", snapshot.Filters[0].Provided)
	require.False(t, snapshot.Filters[0].Enabled)
	key := url.PathEscape(snapshot.Filters[0].Key)
	{
		code, _, _ := call(t, server, admin, http.MethodPut, "/api/filters/"+key,
			config.Filter{Enabled: true, Directive: "DOMAIN", Params: []string{"ignored.test"}})
		require.Equal(t, http.StatusOK, code)
		snapshot := server.store.Snapshot()
		require.True(t, snapshot.Filters[0].Enabled)
		require.Equal(t, []string{"10.55.0.0/16"}, snapshot.Filters[0].Params)
		code, body, _ := call(t, server, admin, http.MethodDelete, "/api/filters/"+key, nil)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "provided filter is read-only", body["error"])
	}
}

func TestStatic_Files(t *testing.T) {
	server := makeServer(t)
	assets, err := fs.Glob(web.Dist, "dist/assets/*.js")
	require.NoError(t, err)
	require.NotEmpty(t, assets)
	{
		recorder := fetch(server, "/")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
		require.Contains(t, recorder.Body.String(), "<div id=\"app\">")
	}
	{
		recorder := fetch(server, "/unknown/page")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
		require.Contains(t, recorder.Body.String(), "<div id=\"app\">")
	}
	{
		recorder := fetch(server, "/favicon.svg")
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Empty(t, recorder.Header().Get("Cache-Control"))
		require.Contains(t, recorder.Body.String(), "<svg")
	}
	{
		recorder := fetch(server, strings.TrimPrefix(assets[0], "dist"))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "public, max-age=31536000, immutable",
			recorder.Header().Get("Cache-Control"))
	}
	{
		recorder := fetch(server, "/api/unknown")
		require.Equal(t, http.StatusNotFound, recorder.Code)
	}
}
