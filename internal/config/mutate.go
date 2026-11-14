package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

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
	result.Users = slices.Clone(config.Users)
	result.Endpoints = slices.Clone(config.Endpoints)
	result.Fallbacks = slices.Clone(config.Fallbacks)
	result.Filters = slices.Clone(config.Filters)
	for index := range result.Endpoints {
		result.Endpoints[index] = result.Endpoints[index].Clone()
	}
	for index := range result.Fallbacks {
		result.Fallbacks[index].Endpoints = slices.Clone(result.Fallbacks[index].Endpoints)
	}
	for index := range result.Filters {
		result.Filters[index].Params = slices.Clone(result.Filters[index].Params)
	}
	return result
}

func (config *Config) TargetExists(target string) bool {
	return target == TargetDirect || target == TargetReject || config.findEndpoint(target) >= 0 ||
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
	for index := range config.Endpoints {
		endpoint := &config.Endpoints[index]
		if endpoint.normalize(); config.findEndpoint(endpoint.ID) != index {
			return fmt.Errorf("duplicate endpoint ID '%v'", endpoint.ID)
		} else if err := endpoint.validate(); err != nil {
			return fmt.Errorf("failed to check endpoint '%v': %w", endpoint.ID, err)
		}
		for _, other := range config.Endpoints[:index] {
			if other.Slot == endpoint.Slot {
				return fmt.Errorf("duplicate endpoint slot (%v)", endpoint.Slot)
			}
		}
	}
	for index := range config.Fallbacks {
		fallback := &config.Fallbacks[index]
		if fallback.normalize(); config.findFallback(fallback.ID) != index ||
			config.findEndpoint(fallback.ID) >= 0 {
			return fmt.Errorf("duplicate fallback ID '%v'", fallback.ID)
		} else if err := fallback.validate(config); err != nil {
			return fmt.Errorf("failed to check fallback '%v': %w", fallback.ID, err)
		}
	}
	for index := range config.Filters {
		filter := &config.Filters[index]
		if filter.normalize(); config.findFilter(filter.Key) != index || filter.Key == "" {
			return fmt.Errorf("duplicate filter key '%v'", filter.Key)
		} else if err := filter.validate(); err != nil {
			return fmt.Errorf("failed to check filter %v: %w", index+1, err)
		}
	}
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

func (config *Config) AddEndpoint(endpoint Endpoint) (Endpoint, error) {
	if TypeLabel(endpoint.Type) == "" {
		return Endpoint{}, fmt.Errorf("unexpected endpoint type '%v'", endpoint.Type)
	}
	if endpoint.normalize(); endpoint.ID == "" {
		endpoint.ID = config.NextID(TypeLabel(endpoint.Type))
	}
	if endpoint.Slot = config.freeSlot(); config.TargetExists(endpoint.ID) {
		return Endpoint{}, fmt.Errorf("ID '%v' already exists", endpoint.ID)
	} else if err := endpoint.validate(); err != nil {
		return Endpoint{}, err
	}
	config.Endpoints = append(config.Endpoints, endpoint)
	return endpoint, nil
}

func (config *Config) UpdateEndpoint(id string, endpoint Endpoint) error {
	index := config.findEndpoint(id)
	if index < 0 {
		return fmt.Errorf("endpoint '%v' does not exist", id)
	}
	current := config.Endpoints[index]
	endpoint.ID, endpoint.Type, endpoint.Slot = current.ID, current.Type, current.Slot
	if endpoint.normalize(); endpoint.Type == "" {
		return errors.New("endpoint type does not exist")
	} else if err := endpoint.validate(); err != nil {
		return err
	}
	config.Endpoints[index] = endpoint
	return nil
}

func (config *Config) RemoveEndpoint(id string) error {
	index := config.findEndpoint(id)
	if index < 0 {
		return fmt.Errorf("endpoint '%v' does not exist", id)
	}
	config.Endpoints = slices.Delete(config.Endpoints, index, index+1)
	for index := range config.Fallbacks {
		fallback := &config.Fallbacks[index]
		fallback.Endpoints = slices.DeleteFunc(fallback.Endpoints, func(member string) bool {
			return member == id
		})
	}
	config.Filters = slices.DeleteFunc(config.Filters, func(filter Filter) bool {
		return filter.Provided == id
	})
	if config.Settings.DefaultTarget == id {
		config.Settings.DefaultTarget = TargetDirect
	}
	return nil
}

func (config *Config) MoveEndpoint(id string, delta int) error {
	return moveItem(config.Endpoints, config.findEndpoint(id), delta)
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
	fallback.ID = id
	fallback.normalize()
	if err := fallback.validate(config); err != nil {
		return err
	}
	config.Fallbacks[index] = fallback
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

func (config *Config) AddFilter(filter Filter) (Filter, error) {
	key, err := makeKey()
	if err != nil {
		return Filter{}, err
	}
	filter.Key, filter.Provided = key, ""
	if err = config.prepareFilter(&filter); err != nil {
		return Filter{}, err
	}
	config.Filters = append(config.Filters, filter)
	return filter, nil
}

func (config *Config) UpdateFilter(key string, filter Filter) error {
	index := config.findFilter(key)
	if index < 0 {
		return fmt.Errorf("filter '%v' does not exist", key)
	} else if current := config.Filters[index]; current.Provided != "" {
		config.Filters[index].Enabled = filter.Enabled
		return nil
	}
	filter.Key, filter.Provided = key, ""
	if err := config.prepareFilter(&filter); err != nil {
		return err
	}
	config.Filters[index] = filter
	return nil
}

func (config *Config) RemoveFilter(key string) error {
	index := config.findFilter(key)
	if index < 0 {
		return fmt.Errorf("filter '%v' does not exist", key)
	} else if config.Filters[index].Provided != "" {
		return errors.New("provided filter is read-only")
	}
	config.Filters = slices.Delete(config.Filters, index, index+1)
	return nil
}

func (config *Config) MoveFilter(key string, delta int) error {
	return moveItem(config.Filters, config.findFilter(key), delta)
}

func (config *Config) SetRoutes(id string, routes []string) (bool, error) {
	index := slices.IndexFunc(config.Filters, func(filter Filter) bool {
		return filter.Provided == id
	})
	if len(routes) == 0 && index < 0 {
		return false, nil
	} else if len(routes) == 0 {
		config.Filters = slices.Delete(config.Filters, index, index+1)
		return true, nil
	} else if index >= 0 && slices.Equal(config.Filters[index].Params, routes) {
		return false, nil
	} else if index >= 0 {
		config.Filters[index].Params = slices.Clone(routes)
		return true, nil
	}
	key, err := makeKey()
	if err != nil {
		return false, err
	}
	config.Filters = append(config.Filters, Filter{Key: key, ID: "Routes of " + id,
		Directive: rules.IPCIDR, Target: id, Params: slices.Clone(routes), Provided: id})
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

func (config *Config) FindEndpoint(id string) (Endpoint, bool) {
	if index := config.findEndpoint(id); index >= 0 {
		return config.Endpoints[index], true
	}
	return Endpoint{}, false
}

func (config *Config) checkAdministrator() error {
	for _, user := range config.Users {
		if user.Role == RoleAdministrator {
			return nil
		}
	}
	return errors.New("last administrator cannot be removed")
}

func (config *Config) prepareFilter(filter *Filter) error {
	if filter.normalize(); !config.TargetExists(filter.Target) {
		return fmt.Errorf("target '%v' does not exist", filter.Target)
	} else if len(filter.ID) > 64 {
		return errors.New("filter ID is too long")
	}
	parsed, err := rules.Parse(filter.Directive, filter.Params)
	if err != nil {
		return fmt.Errorf("failed to check rule parameters: %w", err)
	}
	filter.Params = filter.Params[:0]
	for _, rule := range parsed {
		filter.Params = append(filter.Params, rule.Text())
	}
	return nil
}

func (config *Config) freeSlot() int {
	for slot := 1; ; slot++ {
		if !slices.ContainsFunc(config.Endpoints, func(endpoint Endpoint) bool {
			return endpoint.Slot == slot
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

func (config *Config) findEndpoint(id string) int {
	return slices.IndexFunc(config.Endpoints, func(endpoint Endpoint) bool {
		return endpoint.ID == id
	})
}

func (config *Config) findFallback(id string) int {
	return slices.IndexFunc(config.Fallbacks, func(fallback Fallback) bool {
		return fallback.ID == id
	})
}

func (config *Config) findFilter(key string) int {
	return slices.IndexFunc(config.Filters, func(filter Filter) bool {
		return filter.Key == key
	})
}

func (endpoint Endpoint) Clone() Endpoint {
	if endpoint.Host != nil {
		host := *endpoint.Host
		endpoint.Host = &host
	}
	if endpoint.Socks5 != nil {
		socks := *endpoint.Socks5
		endpoint.Socks5 = &socks
	}
	if endpoint.HTTP != nil {
		proxy := *endpoint.HTTP
		endpoint.HTTP = &proxy
	}
	if endpoint.OpenVPN != nil {
		options := *endpoint.OpenVPN
		endpoint.OpenVPN = &options
	}
	if endpoint.AmneziaWG != nil {
		options := *endpoint.AmneziaWG
		endpoint.AmneziaWG = &options
	}
	if endpoint.Monitoring != nil {
		monitoring := *endpoint.Monitoring
		endpoint.Monitoring = &monitoring
	}
	return endpoint
}
