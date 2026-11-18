package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"proximity/internal/rules"
)

func moveItem[T any](items []T, index int, delta int) error {
	if index < 0 {
		return errors.New("item does not exist")
	} else if delta != -1 && delta != 1 {
		return fmt.Errorf("invalid move direction (%v)", delta)
	} else if target := index + delta; target >= 0 && target < len(items) {
		items[index], items[target] = items[target], items[index]
	}
	return nil
}

func makeKey() (string, error) {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("failed to read random data: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func (config *Config) Clone() Config {
	result := *config
	result.Settings.TimeServers = slices.Clone(config.Settings.TimeServers)
	result.Users = slices.Clone(config.Users)
	result.Outbounds = slices.Clone(config.Outbounds)
	result.Fallbacks = slices.Clone(config.Fallbacks)
	result.Routes = slices.Clone(config.Routes)
	result.Inbounds = slices.Clone(config.Inbounds)
	for index := range result.Inbounds {
		result.Inbounds[index] = result.Inbounds[index].Clone()
	}
	for index := range result.Outbounds {
		result.Outbounds[index] = result.Outbounds[index].Clone()
	}
	for index := range result.Fallbacks {
		result.Fallbacks[index].Outbounds = slices.Clone(result.Fallbacks[index].Outbounds)
	}
	for index := range result.Routes {
		result.Routes[index].Params = slices.Clone(result.Routes[index].Params)
	}
	return result
}

func (config *Config) TargetExists(target string) bool {
	return target == TargetDirect || target == TargetReject || config.findOutbound(target) >= 0 ||
		config.findFallback(target) >= 0
}

func (config *Config) Validate() error {
	for index := range config.Users {
		if err := config.Users[index].validate(); err != nil {
			return err
		} else if config.findUser(config.Users[index].Name) != index {
			return fmt.Errorf("duplicate user '%v'", config.Users[index].Name)
		}
	}
	for index := range config.Outbounds {
		outbound := &config.Outbounds[index]
		if outbound.normalize(); config.findOutbound(outbound.ID) != index {
			return fmt.Errorf("duplicate outbound ID '%v'", outbound.ID)
		} else if err := outbound.validate(); err != nil {
			return fmt.Errorf("failed to check outbound '%v': %w", outbound.ID, err)
		}
		for _, other := range config.Outbounds[:index] {
			if other.Slot == outbound.Slot {
				return fmt.Errorf("duplicate outbound slot (%v)", outbound.Slot)
			}
		}
	}
	for index := range config.Fallbacks {
		fallback := &config.Fallbacks[index]
		if fallback.normalize(); config.findFallback(fallback.ID) != index ||
			config.findOutbound(fallback.ID) >= 0 {
			return fmt.Errorf("duplicate fallback ID '%v'", fallback.ID)
		} else if err := fallback.validate(config); err != nil {
			return fmt.Errorf("failed to check fallback '%v': %w", fallback.ID, err)
		}
	}
	for index := range config.Routes {
		route := &config.Routes[index]
		if route.normalize(); config.findRoute(route.Key) != index || route.Key == "" {
			return fmt.Errorf("duplicate route key '%v'", route.Key)
		} else if config.findRouteID(route.ID) != index {
			return fmt.Errorf("duplicate route ID '%v'", route.ID)
		} else if err := route.validate(); err != nil {
			return fmt.Errorf("failed to check route %v: %w", index+1, err)
		}
	}
	for index := range config.Inbounds {
		inbound := &config.Inbounds[index]
		if inbound.normalize(); config.findInbound(inbound.ID) != index {
			return fmt.Errorf("duplicate inbound ID '%v'", inbound.ID)
		} else if err := inbound.validate(); err != nil {
			return fmt.Errorf("failed to check inbound '%v': %w", inbound.ID, err)
		}
		current, routed := inbound.network()
		for _, other := range config.Inbounds[:index] {
			if other.Port == inbound.Port {
				return fmt.Errorf("duplicate inbound port (%v)", inbound.Port)
			} else if prefix, found := other.network(); found && routed &&
				prefix.Overlaps(current) {
				return fmt.Errorf("network of inbound '%v' overlaps inbound '%v'", inbound.ID,
					other.ID)
			}
		}
	}
	config.Settings.normalize()
	if err := config.Settings.validate(config); err != nil {
		return fmt.Errorf("failed to check settings: %w", err)
	}
	return nil
}

func (config *Config) NextID(label string) string {
	for index := 1; ; index++ {
		if id := fmt.Sprintf("%v %v", label, index); !config.TargetExists(id) {
			return id
		}
	}
}

func (config *Config) NextRouteID() string {
	for index := 1; ; index++ {
		if id := fmt.Sprintf("Route %v", index); config.findRouteID(id) < 0 {
			return id
		}
	}
}

func (config *Config) AddOutbound(outbound Outbound) (Outbound, error) {
	if TypeLabel(outbound.Type) == "" {
		return Outbound{}, fmt.Errorf("unexpected outbound type '%v'", outbound.Type)
	}
	if outbound.normalize(); outbound.ID == "" {
		outbound.ID = config.NextID(TypeLabel(outbound.Type))
	}
	if outbound.Slot = config.freeSlot(); config.TargetExists(outbound.ID) {
		return Outbound{}, fmt.Errorf("ID '%v' already exists", outbound.ID)
	} else if err := outbound.validate(); err != nil {
		return Outbound{}, err
	}
	config.Outbounds = append(config.Outbounds, outbound)
	return outbound, nil
}

func (config *Config) UpdateOutbound(id string, outbound Outbound) error {
	index := config.findOutbound(id)
	if index < 0 {
		return fmt.Errorf("outbound '%v' does not exist", id)
	}
	current := config.Outbounds[index]
	outbound.Type, outbound.Slot = current.Type, current.Slot
	if outbound.normalize(); outbound.ID == "" {
		outbound.ID = id
	}
	if outbound.Type == "" {
		return errors.New("outbound type does not exist")
	} else if outbound.ID != id && config.TargetExists(outbound.ID) {
		return fmt.Errorf("ID '%v' already exists", outbound.ID)
	} else if err := outbound.validate(); err != nil {
		return err
	}
	config.Outbounds[index] = outbound
	config.renameTarget(id, outbound.ID)
	return nil
}

func (config *Config) RemoveOutbound(id string) error {
	index := config.findOutbound(id)
	if index < 0 {
		return fmt.Errorf("outbound '%v' does not exist", id)
	}
	config.Outbounds = slices.Delete(config.Outbounds, index, index+1)
	for index := range config.Fallbacks {
		fallback := &config.Fallbacks[index]
		fallback.Outbounds = slices.DeleteFunc(fallback.Outbounds, func(member string) bool {
			return member == id
		})
	}
	config.Routes = slices.DeleteFunc(config.Routes, func(route Route) bool {
		return route.Provided == id
	})
	if config.Settings.DefaultTarget == id {
		config.Settings.DefaultTarget = TargetDirect
	}
	return nil
}

func (config *Config) MoveOutbound(id string, delta int) error {
	return moveItem(config.Outbounds, config.findOutbound(id), delta)
}

func (config *Config) AddFallback(fallback Fallback) (Fallback, error) {
	if fallback.normalize(); fallback.ID == "" {
		fallback.ID = config.NextID("Fallback")
	}
	if config.TargetExists(fallback.ID) {
		return Fallback{}, fmt.Errorf("ID '%v' already exists", fallback.ID)
	} else if err := fallback.validate(config); err != nil {
		return Fallback{}, err
	}
	config.Fallbacks = append(config.Fallbacks, fallback)
	return fallback, nil
}

func (config *Config) UpdateFallback(id string, fallback Fallback) error {
	index := config.findFallback(id)
	if index < 0 {
		return fmt.Errorf("fallback '%v' does not exist", id)
	}
	if fallback.normalize(); fallback.ID == "" {
		fallback.ID = id
	}
	if fallback.ID != id && config.TargetExists(fallback.ID) {
		return fmt.Errorf("ID '%v' already exists", fallback.ID)
	} else if err := fallback.validate(config); err != nil {
		return err
	}
	config.Fallbacks[index] = fallback
	config.renameTarget(id, fallback.ID)
	return nil
}

func (config *Config) RemoveFallback(id string) error {
	index := config.findFallback(id)
	if index < 0 {
		return fmt.Errorf("fallback '%v' does not exist", id)
	}
	config.Fallbacks = slices.Delete(config.Fallbacks, index, index+1)
	if config.Settings.DefaultTarget == id {
		config.Settings.DefaultTarget = TargetDirect
	}
	return nil
}

func (config *Config) MoveFallback(id string, delta int) error {
	return moveItem(config.Fallbacks, config.findFallback(id), delta)
}

func (config *Config) AddRoute(route Route) (Route, error) {
	key, err := makeKey()
	if err != nil {
		return Route{}, err
	}
	route.Key, route.Provided = key, ""
	if err = config.prepareRoute(&route, -1); err != nil {
		return Route{}, err
	}
	config.Routes = append(config.Routes, route)
	return route, nil
}

func (config *Config) UpdateRoute(key string, route Route) error {
	index := config.findRoute(key)
	if index < 0 {
		return fmt.Errorf("route '%v' does not exist", key)
	} else if current := config.Routes[index]; current.Provided != "" {
		return errors.New("provided route follows its VPN connection")
	}
	route.Key, route.Provided = key, ""
	if err := config.prepareRoute(&route, index); err != nil {
		return err
	}
	config.Routes[index] = route
	return nil
}

func (config *Config) RemoveRoute(key string) error {
	index := config.findRoute(key)
	if index < 0 {
		return fmt.Errorf("route '%v' does not exist", key)
	} else if config.Routes[index].Provided != "" {
		return errors.New("provided route is read-only")
	}
	config.Routes = slices.Delete(config.Routes, index, index+1)
	return nil
}

func (config *Config) MoveRoute(key string, delta int) error {
	return moveItem(config.Routes, config.findRoute(key), delta)
}

// NOTE: The route of a VPN connection exists while the outbound receives routes. It is on
// while the connection is up and has given its routes, and keeps its place in the list and
// its last networks while the connection is down.
func (config *Config) SetRoutes(id string, wanted bool, active bool, networks []string) (bool,
	error) {
	index := slices.IndexFunc(config.Routes, func(route Route) bool {
		return route.Provided == id
	})
	enabled := wanted && active && len(networks) != 0
	if !wanted && index < 0 || index < 0 && !enabled {
		return false, nil
	} else if !wanted {
		config.Routes = slices.Delete(config.Routes, index, index+1)
		return true, nil
	} else if index >= 0 {
		route := &config.Routes[index]
		changed := route.Enabled != enabled || (enabled && !slices.Equal(route.Params, networks))
		if route.Enabled = enabled; enabled {
			route.Params = slices.Clone(networks)
		}
		return changed, nil
	}
	key, err := makeKey()
	if err != nil {
		return false, err
	}
	route := Route{Key: key, ID: providedPrefix + id, Directive: rules.IPCIDR, Target: id,
		Params: slices.Clone(networks), Provided: id, Enabled: true}
	if config.findRouteID(route.ID) >= 0 {
		route.ID = config.NextRouteID()
	}
	config.Routes = slices.Insert(config.Routes, 0, route)
	return true, nil
}

func (config *Config) AddUser(user User) error {
	if err := user.validate(); err != nil {
		return err
	} else if config.findUser(user.Name) >= 0 {
		return fmt.Errorf("user '%v' already exists", user.Name)
	}
	config.Users = append(config.Users, user)
	return nil
}

func (config *Config) UpdateUser(name string, role string, hash string) error {
	index := config.findUser(name)
	if index < 0 {
		return fmt.Errorf("user '%v' does not exist", name)
	}
	user := config.Users[index]
	if role != "" {
		user.Role = role
	}
	if hash != "" {
		user.PasswordHash = hash
		user.Epoch++
	}
	if err := user.validate(); err != nil {
		return err
	}
	config.Users[index] = user
	return config.checkAdministrator()
}

func (config *Config) RemoveUser(name string) error {
	index := config.findUser(name)
	if index < 0 {
		return fmt.Errorf("user '%v' does not exist", name)
	}
	config.Users = slices.Delete(config.Users, index, index+1)
	return config.checkAdministrator()
}

func (config *Config) FindUser(name string) (User, bool) {
	if index := config.findUser(name); index >= 0 {
		return config.Users[index], true
	}
	return User{}, false
}

func (config *Config) FindOutbound(id string) (Outbound, bool) {
	if index := config.findOutbound(id); index >= 0 {
		return config.Outbounds[index], true
	}
	return Outbound{}, false
}

func (config *Config) checkAdministrator() error {
	for _, user := range config.Users {
		if user.Role == RoleAdministrator {
			return nil
		}
	}
	return errors.New("last administrator cannot be removed")
}

func (config *Config) renameTarget(previous string, current string) {
	if previous == current {
		return
	}
	for index := range config.Fallbacks {
		for position, member := range config.Fallbacks[index].Outbounds {
			if member == previous {
				config.Fallbacks[index].Outbounds[position] = current
			}
		}
	}
	for index := range config.Routes {
		route := &config.Routes[index]
		if route.Target == previous {
			route.Target = current
		}
		if route.Provided != previous {
			continue
		} else if route.Provided = current; route.ID == providedPrefix+previous &&
			config.findRouteID(providedPrefix+current) < 0 {
			route.ID = providedPrefix + current
		}
	}
	for index := range config.Inbounds {
		if config.Inbounds[index].Outbound == previous {
			config.Inbounds[index].Outbound = current
		}
	}
	if config.Settings.DefaultTarget == previous {
		config.Settings.DefaultTarget = current
	}
}

func (config *Config) prepareRoute(route *Route, index int) error {
	if route.normalize(); route.ID == "" && index >= 0 {
		route.ID = config.Routes[index].ID
	} else if route.ID == "" {
		route.ID = config.NextRouteID()
	}
	if found := config.findRouteID(route.ID); found >= 0 && found != index {
		return fmt.Errorf("route ID '%v' already exists", route.ID)
	} else if !config.TargetExists(route.Target) {
		return fmt.Errorf("target '%v' does not exist", route.Target)
	} else if len(route.ID) > 64 {
		return errors.New("route ID is too long")
	}
	_, params, err := rules.ParseText(route.Directive, route.Params)
	if err != nil {
		return fmt.Errorf("failed to check rule parameters: %w", err)
	}
	route.Params = params
	return nil
}

func (config *Config) upgrade() error {
	config.migrate()
	return config.Validate()
}

func (config *Config) migrate() {
	if config.adopt(); config.Version < 2 {
		config.Settings.ConnectivityChecks, config.Settings.Capture = true, true
		for index := range config.Fallbacks {
			config.Fallbacks[index].HealthCheck = true
		}
	}
	if config.Version < autostartVersion {
		config.Settings.Autostart = true
	}
	for index := range config.Outbounds {
		outbound := &config.Outbounds[index]
		outbound.Autostart = outbound.Autostart || config.Version < serviceVersion
		if host := outbound.Host; host != nil && config.Version < attemptsVersion {
			outbound.Attempts, outbound.ReconnectTimeout = host.LegacyAttempts, host.LegacyTimeout
		} else if monitoring := outbound.Monitoring; monitoring != nil &&
			config.Version < attemptsVersion {
			outbound.Attempts = monitoring.LegacyAttempts + 1
			outbound.ReconnectTimeout = monitoring.LegacyDelay
		}
	}
	for index := range config.Inbounds {
		config.Inbounds[index].Autostart = config.Inbounds[index].Autostart ||
			config.Version < serviceVersion
	}
	for _, route := range config.Routes {
		// NOTE: A provided route that was switched on by hand becomes "receive routes".
		index := config.findOutbound(route.Provided)
		if route.Provided == "" || !route.Enabled || index < 0 || config.Version >= routesVersion {
			continue
		} else if options := config.Outbounds[index].OpenVPN; options != nil {
			options.UseRoutes = true
		} else if tunnel := config.Outbounds[index].AmneziaWG; tunnel != nil {
			tunnel.UseRoutes = true
		}
	}
	for index := range config.Routes {
		route := &config.Routes[index]
		if route.ID = strings.TrimSpace(route.ID); route.ID == "" ||
			config.findRouteID(route.ID) != index {
			route.ID = config.NextRouteID()
		}
	}
	if config.Inbounds == nil {
		config.Inbounds = []Inbound{}
	}
	config.Version = currentVersion
}

func (config *Config) adopt() {
	if len(config.Outbounds) == 0 && config.Legacy.Endpoints != nil {
		config.Outbounds = config.Legacy.Endpoints
	}
	if len(config.Routes) == 0 && config.Legacy.Filters != nil {
		config.Routes = config.Legacy.Filters
	}
	config.Legacy = Legacy{}
	for index := range config.Fallbacks {
		fallback := &config.Fallbacks[index]
		if len(fallback.Outbounds) == 0 && fallback.Legacy != nil {
			fallback.Outbounds = fallback.Legacy
		}
		fallback.Legacy = nil
	}
}

func (config *Config) freeSlot() int {
	for slot := 1; ; slot++ {
		if !slices.ContainsFunc(config.Outbounds, func(outbound Outbound) bool {
			return outbound.Slot == slot
		}) {
			return slot
		}
	}
}

func (config *Config) findUser(name string) int {
	return slices.IndexFunc(config.Users, func(user User) bool {
		return user.Name == name
	})
}

func (config *Config) findOutbound(id string) int {
	return slices.IndexFunc(config.Outbounds, func(outbound Outbound) bool {
		return outbound.ID == id
	})
}

func (config *Config) findFallback(id string) int {
	return slices.IndexFunc(config.Fallbacks, func(fallback Fallback) bool {
		return fallback.ID == id
	})
}

func (config *Config) findRoute(key string) int {
	return slices.IndexFunc(config.Routes, func(route Route) bool {
		return route.Key == key
	})
}

func (config *Config) findRouteID(id string) int {
	return slices.IndexFunc(config.Routes, func(route Route) bool {
		return route.ID == id
	})
}

func (outbound Outbound) Clone() Outbound {
	if outbound.Host != nil {
		host := *outbound.Host
		outbound.Host = &host
	}
	if outbound.Socks5 != nil {
		socks := *outbound.Socks5
		outbound.Socks5 = &socks
	}
	if outbound.HTTP != nil {
		proxy := *outbound.HTTP
		outbound.HTTP = &proxy
	}
	if outbound.OpenVPN != nil {
		options := *outbound.OpenVPN
		outbound.OpenVPN = &options
	}
	if outbound.AmneziaWG != nil {
		options := *outbound.AmneziaWG
		outbound.AmneziaWG = &options
	}
	if outbound.Monitoring != nil {
		monitoring := *outbound.Monitoring
		outbound.Monitoring = &monitoring
	}
	if outbound.Recovery != nil {
		recovery := *outbound.Recovery
		outbound.Recovery = &recovery
	}
	return outbound
}
