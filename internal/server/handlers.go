package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"proximity/internal/auth"
	"proximity/internal/config"
	"proximity/internal/link"
	"proximity/internal/router"
	"proximity/internal/rules"
	"proximity/internal/wgconf"
)

type endpointView struct {
	config.Endpoint
	Status   link.Status    `json:"status"`
	Traffic  router.Traffic `json:"traffic"`
	Usage    string         `json:"usage"`
	Redacted bool           `json:"redacted"`
}

type targetView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Enabled bool   `json:"enabled"`
}

type userView struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type passwordRequest struct {
	Current  string `json:"current"`
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

type userRequest struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

type moveRequest struct {
	Delta int `json:"delta"`
}

type importRequest struct {
	Text string `json:"text"`
}

type targetRequest struct {
	Target string `json:"target"`
}

const (
	usageEnabled  = "enabled"
	usageDisabled = "disabled"
	usageUnused   = "unused"
)

func redact(endpoint config.Endpoint) config.Endpoint {
	endpoint = endpoint.Clone()
	if endpoint.Socks5 != nil {
		endpoint.Socks5.Password = ""
	}
	if endpoint.HTTP != nil {
		endpoint.HTTP.Password = ""
	}
	if endpoint.OpenVPN != nil {
		endpoint.OpenVPN.Config, endpoint.OpenVPN.Password = "", ""
		endpoint.OpenVPN.OTPKey, endpoint.OpenVPN.KeyPassword = "", ""
	}
	if endpoint.AmneziaWG != nil {
		endpoint.AmneziaWG.Config = ""
	}
	return endpoint
}

func confirm(password string, repeated string) error {
	if password != repeated {
		return errors.New("passwords do not match")
	}
	return nil
}

func emptyStatus() link.Status {
	return link.Status{State: link.StateDisabled, Latency: -1, DNS: []string{},
		Domains: []string{}, Routes: []string{}}
}

func (server *Server) register() {
	server.handle("GET /api/session", 0, server.getSession)
	server.handle("POST /api/setup", 0, server.setup)
	server.handle("POST /api/login", 0, server.login)
	server.handle("POST /api/logout", 0, server.logout)
	server.handle("POST /api/password", 1, server.changePassword)
	server.handle("GET /api/dashboard", 0, server.getDashboard)
	server.handle("DELETE /api/refused", 2, server.clearRefused)
	server.handle("GET /api/targets", 1, server.getTargets)
	server.handle("GET /api/interfaces", 1, server.getInterfaces)
	server.handle("GET /api/endpoints", 1, server.getEndpoints)
	server.handle("POST /api/endpoints", 2, server.addEndpoint)
	server.handle("PUT /api/endpoints/{id}", 2, server.updateEndpoint)
	server.handle("DELETE /api/endpoints/{id}", 2, server.removeEndpoint)
	server.handle("POST /api/endpoints/{id}/move", 2, server.moveEndpoint)
	server.handle("POST /api/endpoints/{id}/restart", 2, server.restartEndpoint)
	server.handle("GET /api/endpoints/{id}/log", 2, server.getEndpointLog)
	server.handle("POST /api/import/amneziawg", 2, server.importAmnezia)
	server.handle("GET /api/fallbacks", 1, server.getFallbacks)
	server.handle("POST /api/fallbacks", 2, server.addFallback)
	server.handle("PUT /api/fallbacks/{id}", 2, server.updateFallback)
	server.handle("DELETE /api/fallbacks/{id}", 2, server.removeFallback)
	server.handle("POST /api/fallbacks/{id}/move", 2, server.moveFallback)
	server.handle("GET /api/filters", 1, server.getFilters)
	server.handle("POST /api/filters", 2, server.addFilter)
	server.handle("POST /api/filters/preview", 1, server.previewFilter)
	server.handle("PUT /api/filters/default", 2, server.setDefaultTarget)
	server.handle("PUT /api/filters/{key}", 2, server.updateFilter)
	server.handle("DELETE /api/filters/{key}", 2, server.removeFilter)
	server.handle("POST /api/filters/{key}/move", 2, server.moveFilter)
	server.handle("GET /api/settings", 1, server.getSettings)
	server.handle("PUT /api/settings", 3, server.updateSettings)
	server.handle("GET /api/users", 3, server.getUsers)
	server.handle("POST /api/users", 3, server.addUser)
	server.handle("PUT /api/users/{name}", 3, server.updateUser)
	server.handle("DELETE /api/users/{name}", 3, server.removeUser)
	server.handle("GET /api/router/config", 2, server.getRouterConfig)
	server.handle("GET /api/backup", 2, server.exportBackup)
	server.handle("POST /api/backup", 2, server.importBackup)
	server.routes.Handle("/", server.static())
}

func (server *Server) problems() ([]string, string) {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	return append([]string{}, server.warnings...), server.failure
}

func (server *Server) capturing() bool {
	server.mutex.Lock()
	defer server.mutex.Unlock()
	return server.captured
}

func (server *Server) views(snapshot config.Config, level int) []endpointView {
	links, used := server.manager.Status(), router.Used(snapshot)
	traffic, _ := server.tracker.Snapshot()
	result := []endpointView{}
	for _, endpoint := range snapshot.Endpoints {
		view := endpointView{Endpoint: endpoint, Status: emptyStatus(), Usage: usageEnabled,
			Traffic: traffic[endpoint.ID], Redacted: level < 2}
		if status, found := links[endpoint.ID]; found {
			view.Status = status
		}
		if !endpoint.Enabled {
			view.Usage = usageDisabled
		} else if !used[endpoint.ID] {
			view.Usage = usageUnused
		}
		if level < 2 {
			view.Endpoint = redact(endpoint)
		}
		if level < 1 {
			state, since := view.Status.State, view.Status.Since
			view.Status = emptyStatus()
			view.Status.State, view.Status.Since = state, since
			view.Status.Link, view.Status.Latency = links[endpoint.ID].Link, -1
			view.Endpoint = config.Endpoint{ID: endpoint.ID, Name: endpoint.Name,
				Group: endpoint.Group, Type: endpoint.Type, Enabled: endpoint.Enabled,
				Statistics: endpoint.Statistics}
			if found := links[endpoint.ID].Checked != 0; found {
				view.Status.Latency = links[endpoint.ID].Latency
			}
		}
		result = append(result, view)
	}
	return result
}

func (server *Server) getSession(_ *http.Request, current session) (any, error) {
	role, snapshot := current.user.Role, server.store.Snapshot()
	if current.level == 0 {
		role = config.RoleAnonymous
	}
	return map[string]any{"user": current.user.Name, "role": role, "level": current.level,
		"version": server.options.Version, "setup": len(snapshot.Users) == 0,
		"host_network":        server.options.HostNetwork,
		"anonymous_dashboard": snapshot.Settings.AnonymousDashboard}, nil
}

func (server *Server) setup(request *http.Request, _ session) (any, error) {
	body := setupRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if len(server.store.Snapshot().Users) != 0 {
		return nil, fail(http.StatusForbidden, "administrator already exists")
	} else if err = confirm(body.Password, body.Confirm); err != nil {
		return nil, err
	} else if err = auth.CreateAdministrator(server.store, body.Username, body.Password); err != nil {
		return nil, err
	}
	server.options.Logf("created administrator '%v'", body.Username)
	return server.authorize(body.Username)
}

func (server *Server) login(request *http.Request, _ session) (any, error) {
	body := loginRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	snapshot := server.store.Snapshot()
	user, found := snapshot.FindUser(body.Username)
	if !found || !auth.VerifyPassword(user.PasswordHash, body.Password) {
		time.Sleep(failedLoginDelay)
		return nil, fail(http.StatusUnauthorized, "invalid user name or password")
	}
	return server.authorize(body.Username)
}

func (server *Server) authorize(name string) (any, error) {
	snapshot := server.store.Snapshot()
	user, found := snapshot.FindUser(name)
	if !found {
		return nil, fmt.Errorf("user '%v' does not exist", name)
	}
	expires := time.Now().Add(sessionLifetime)
	token, err := auth.MakeToken(snapshot.Secret, user, expires)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode}, nil
}

func (server *Server) logout(_ *http.Request, _ session) (any, error) {
	return &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode}, nil
}

func (server *Server) changePassword(request *http.Request, current session) (any, error) {
	body := passwordRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if !auth.VerifyPassword(current.user.PasswordHash, body.Current) {
		return nil, errors.New("current password is invalid")
	} else if err = confirm(body.Password, body.Confirm); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		return nil, err
	} else if err = server.store.Update(func(config *config.Config) error {
		return config.UpdateUser(current.user.Name, "", hash)
	}); err != nil {
		return nil, err
	}
	return server.logout(request, current)
}

func (server *Server) getDashboard(_ *http.Request, current session) (any, error) {
	snapshot := server.store.Snapshot()
	if current.level == 0 && !snapshot.Settings.AnonymousDashboard {
		return nil, fail(http.StatusUnauthorized, "authentication required")
	}
	traffic, refused := server.tracker.Snapshot()
	warnings, failure := server.problems()
	views, up, enabled, lines := server.views(snapshot, current.level), 0, 0, 0
	for _, view := range views {
		if view.Enabled {
			enabled++
		}
		if view.Status.State == link.StateUp {
			up++
		}
	}
	for _, item := range router.Items(snapshot) {
		lines += len(item.Rules)
	}
	if current.level == 0 {
		refused, warnings = []router.Refused{}, []string{}
	}
	return map[string]any{"endpoints": views, "fallbacks": snapshot.Fallbacks, "refused": refused,
		"router": server.mihomo.State(), "warnings": warnings, "failure": failure,
		"total": traffic[""], "uptime": int64(time.Since(server.started).Seconds()),
		"counts": map[string]int{"endpoints": len(views), "endpoints_enabled": enabled,
			"endpoints_up": up, "fallbacks": len(snapshot.Fallbacks),
			"filters": len(snapshot.Filters), "rules": lines},
		"proxy_port": snapshot.Settings.MixedPort, "capture": server.capturing()}, nil
}

func (server *Server) clearRefused(_ *http.Request, _ session) (any, error) {
	server.tracker.Clear()
	return map[string]bool{"ok": true}, nil
}

func (server *Server) getTargets(_ *http.Request, _ session) (any, error) {
	snapshot := server.store.Snapshot()
	result := []targetView{{config.TargetDirect, "builtin", true},
		{config.TargetReject, "builtin", true}}
	for _, endpoint := range snapshot.Endpoints {
		result = append(result, targetView{endpoint.ID, "endpoint", endpoint.Enabled})
	}
	for _, fallback := range snapshot.Fallbacks {
		result = append(result, targetView{fallback.ID, "fallback", fallback.Enabled})
	}
	return result, nil
}

func (server *Server) getInterfaces(_ *http.Request, _ session) (any, error) {
	return link.Interfaces()
}

func (server *Server) getEndpoints(_ *http.Request, current session) (any, error) {
	snapshot := server.store.Snapshot()
	groups := []string{}
	for _, endpoint := range snapshot.Endpoints {
		found := endpoint.Group == ""
		for _, group := range groups {
			found = found || group == endpoint.Group
		}
		if !found {
			groups = append(groups, endpoint.Group)
		}
	}
	return map[string]any{"endpoints": server.views(snapshot, current.level), "groups": groups}, nil
}

func (server *Server) addEndpoint(request *http.Request, _ session) (any, error) {
	body, result := config.Endpoint{}, config.Endpoint{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = server.store.Update(func(config *config.Config) error {
		var err error
		result, err = config.AddEndpoint(body)
		return err
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (server *Server) updateEndpoint(request *http.Request, _ session) (any, error) {
	body, id := config.Endpoint{}, request.PathValue("id")
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return body, server.store.Update(func(config *config.Config) error {
		if err := config.UpdateEndpoint(id, body); err != nil {
			return err
		} else if renamed := strings.TrimSpace(body.ID); renamed != "" {
			server.tracker.Rename(id, renamed)
		}
		return nil
	})
}

func (server *Server) removeEndpoint(request *http.Request, _ session) (any, error) {
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.RemoveEndpoint(request.PathValue("id"))
	})
}

func (server *Server) moveEndpoint(request *http.Request, _ session) (any, error) {
	body := moveRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.MoveEndpoint(request.PathValue("id"), body.Delta)
	})
}

func (server *Server) restartEndpoint(request *http.Request, _ session) (any, error) {
	return map[string]bool{"ok": true}, server.manager.Restart(request.PathValue("id"))
}

func (server *Server) getEndpointLog(request *http.Request, _ session) (any, error) {
	return map[string]any{"lines": server.manager.Log(request.PathValue("id"))}, nil
}

func (server *Server) importAmnezia(request *http.Request, _ session) (any, error) {
	body := importRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	result, err := wgconf.Import(body.Text)
	if err != nil {
		return nil, err
	}
	return map[string]string{"config": result}, nil
}

func (server *Server) getFallbacks(_ *http.Request, _ session) (any, error) {
	return server.store.Snapshot().Fallbacks, nil
}

func (server *Server) addFallback(request *http.Request, _ session) (any, error) {
	body, result := config.Fallback{}, config.Fallback{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = server.store.Update(func(config *config.Config) error {
		var err error
		result, err = config.AddFallback(body)
		return err
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (server *Server) updateFallback(request *http.Request, _ session) (any, error) {
	body := config.Fallback{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return body, server.store.Update(func(config *config.Config) error {
		return config.UpdateFallback(request.PathValue("id"), body)
	})
}

func (server *Server) removeFallback(request *http.Request, _ session) (any, error) {
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.RemoveFallback(request.PathValue("id"))
	})
}

func (server *Server) moveFallback(request *http.Request, _ session) (any, error) {
	body := moveRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.MoveFallback(request.PathValue("id"), body.Delta)
	})
}

func (server *Server) getFilters(_ *http.Request, _ session) (any, error) {
	snapshot := server.store.Snapshot()
	problems, failure := server.problems()
	return map[string]any{"filters": snapshot.Filters, "directives": rules.Directives,
		"warnings": rules.Check(router.Items(snapshot)), "problems": problems,
		"failure": failure, "default_target": snapshot.Settings.DefaultTarget}, nil
}

func (server *Server) addFilter(request *http.Request, _ session) (any, error) {
	body, result := config.Filter{}, config.Filter{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = server.store.Update(func(config *config.Config) error {
		var err error
		result, err = config.AddFilter(body)
		return err
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (server *Server) previewFilter(request *http.Request, _ session) (any, error) {
	body := config.Filter{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	parsed, err := rules.Parse(body.Directive, body.Params)
	if err != nil {
		return nil, err
	}
	lines, params := []string{}, []string{}
	for _, rule := range parsed {
		lines = append(lines, rule.Lines(body.Target, nil)...)
		params = append(params, rule.Text())
	}
	return map[string]any{"lines": lines, "params": params}, nil
}

func (server *Server) setDefaultTarget(request *http.Request, _ session) (any, error) {
	body := targetRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return body, server.store.Update(func(config *config.Config) error {
		config.Settings.DefaultTarget = body.Target
		return nil
	})
}

func (server *Server) updateFilter(request *http.Request, _ session) (any, error) {
	body := config.Filter{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return body, server.store.Update(func(config *config.Config) error {
		return config.UpdateFilter(request.PathValue("key"), body)
	})
}

func (server *Server) removeFilter(request *http.Request, _ session) (any, error) {
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.RemoveFilter(request.PathValue("key"))
	})
}

func (server *Server) moveFilter(request *http.Request, _ session) (any, error) {
	body := moveRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.MoveFilter(request.PathValue("key"), body.Delta)
	})
}

func (server *Server) getSettings(_ *http.Request, _ session) (any, error) {
	return server.store.Snapshot().Settings, nil
}

func (server *Server) updateSettings(request *http.Request, _ session) (any, error) {
	body := config.Settings{}
	if err := decode(request, &body); err != nil {
		return nil, err
	}
	return body, server.store.Update(func(config *config.Config) error {
		config.Settings = body
		return nil
	})
}

func (server *Server) getUsers(_ *http.Request, _ session) (any, error) {
	result := []userView{}
	for _, user := range server.store.Snapshot().Users {
		result = append(result, userView{user.Name, user.Role})
	}
	return result, nil
}

func (server *Server) addUser(request *http.Request, _ session) (any, error) {
	body := userRequest{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = confirm(body.Password, body.Confirm); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		return nil, err
	}
	user := config.User{Name: body.Name, Role: body.Role, PasswordHash: hash}
	return userView{body.Name, body.Role}, server.store.Update(func(config *config.Config) error {
		return config.AddUser(user)
	})
}

func (server *Server) updateUser(request *http.Request, current session) (any, error) {
	body, hash, name := userRequest{}, "", request.PathValue("name")
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if name == current.user.Name && body.Role != "" && body.Role != current.user.Role {
		return nil, errors.New("own role cannot be changed")
	} else if err = confirm(body.Password, body.Confirm); err != nil {
		return nil, err
	}
	if body.Password != "" {
		var err error
		if hash, err = auth.HashPassword(body.Password); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.UpdateUser(name, body.Role, hash)
	})
}

func (server *Server) removeUser(request *http.Request, current session) (any, error) {
	name := request.PathValue("name")
	if name == current.user.Name {
		return nil, errors.New("own account cannot be removed")
	}
	return map[string]bool{"ok": true}, server.store.Update(func(config *config.Config) error {
		return config.RemoveUser(name)
	})
}

func (server *Server) getRouterConfig(_ *http.Request, _ session) (any, error) {
	return map[string]string{"config": string(server.mihomo.Current())}, nil
}

func (server *Server) exportBackup(_ *http.Request, _ session) (any, error) {
	snapshot := server.store.Snapshot()
	return snapshot.Export(time.Now()), nil
}

func (server *Server) importBackup(request *http.Request, current session) (any, error) {
	body := config.Backup{}
	if err := decode(request, &body); err != nil {
		return nil, err
	} else if err = server.store.Update(func(config *config.Config) error {
		return config.Import(body, current.level >= 3)
	}); err != nil {
		return nil, fmt.Errorf("failed to import backup: %w", err)
	}
	snapshot := server.store.Snapshot()
	server.options.Logf("backup imported by '%v'", current.user.Name)
	return map[string]int{"endpoints": len(snapshot.Endpoints),
		"fallbacks": len(snapshot.Fallbacks), "filters": len(snapshot.Filters)}, nil
}
