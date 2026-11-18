package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

type Problem struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Kept   bool   `json:"kept"`
}

type Problems struct {
	Time   int64     `json:"time"`
	Backup string    `json:"backup"`
	Items  []Problem `json:"items"`
}

type repairState struct {
	items []Problem
}

type itemLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const (
	KindFile     = "file"
	KindSettings = "settings"
	KindUser     = "user"
	KindOutbound = "outbound"
	KindFallback = "fallback"
	KindRoute    = "route"
	KindInbound  = "inbound"
)

const problemsName = "proximity.problems.json"
const backupPattern = "proximity.invalid-%v.json"
const wholeList = "*"

func decodeItems[T any](state *repairState, kind string, data json.RawMessage) []T {
	result, items := []T{}, []json.RawMessage{}
	if len(data) == 0 {
		return result
	} else if err := json.Unmarshal(data, &items); err != nil {
		state.drop(kind, wholeList, err.Error())
		return result
	}
	for index, item := range items {
		var value T
		if err := json.Unmarshal(item, &value); err != nil {
			state.drop(kind, labelOf(item, index), err.Error())
			continue
		}
		result = append(result, value)
	}
	return result
}

func keepValid[T any](state *repairState, result *Config, list *[]T, items []T, kind string,
	nameFunc func(item T) string) {
	for _, item := range items {
		*list = append(*list, item)
		if err := result.Validate(); err != nil {
			state.drop(kind, nameFunc(item), err.Error())
			*list = (*list)[:len(*list)-1]
		}
	}
}

func labelOf(item json.RawMessage, index int) string {
	label := itemLabel{}
	if err := json.Unmarshal(item, &label); err == nil && label.ID != "" {
		return label.ID
	} else if err == nil && label.Name != "" {
		return label.Name
	}
	return "#" + strconv.Itoa(index+1)
}

// NOTE: An element is kept only when the configuration with it is still valid, so one
// broken block does not take the rest with it.
func repair(data []byte) (Config, []Problem) {
	state, fields, source := &repairState{}, map[string]json.RawMessage{}, Default()
	if err := json.Unmarshal(data, &fields); err != nil {
		return source, []Problem{{Kind: KindFile, ID: fileName, Reason: err.Error()}}
	}
	if value, found := fields["version"]; found {
		if err := json.Unmarshal(value, &source.Version); err != nil {
			state.drop(KindSettings, "version", err.Error())
			source.Version = currentVersion
		}
	}
	if value, found := fields["secret"]; found {
		if err := json.Unmarshal(value, &source.Secret); err != nil {
			source.Secret = ""
		}
	}
	if value, found := fields["settings"]; found {
		if err := json.Unmarshal(value, &source.Settings); err != nil {
			state.drop(KindSettings, KindSettings, err.Error())
			source.Settings = Default().Settings
		}
	}
	source.Users = decodeItems[User](state, KindUser, fields["users"])
	source.Outbounds = decodeItems[Outbound](state, KindOutbound, fields["outbounds"])
	source.Fallbacks = decodeItems[Fallback](state, KindFallback, fields["fallbacks"])
	source.Routes = decodeItems[Route](state, KindRoute, fields["routes"])
	source.Inbounds = decodeItems[Inbound](state, KindInbound, fields["inbounds"])
	source.migrate()
	result, target := source, source.Settings.DefaultTarget
	result.Users, result.Outbounds, result.Fallbacks = []User{}, []Outbound{}, []Fallback{}
	result.Routes, result.Inbounds = []Route{}, []Inbound{}
	result.Settings.DefaultTarget = TargetDirect
	if err := result.Validate(); err != nil {
		state.drop(KindSettings, KindSettings, err.Error())
		result.Settings = Default().Settings
	}
	keepValid(state, &result, &result.Users, source.Users, KindUser, func(item User) string {
		return item.Name
	})
	keepValid(state, &result, &result.Outbounds, source.Outbounds, KindOutbound,
		func(item Outbound) string {
			return item.ID
		})
	keepValid(state, &result, &result.Fallbacks, source.Fallbacks, KindFallback,
		func(item Fallback) string {
			return item.ID
		})
	keepValid(state, &result, &result.Routes, source.Routes, KindRoute, func(item Route) string {
		return item.ID
	})
	keepValid(state, &result, &result.Inbounds, source.Inbounds, KindInbound,
		func(item Inbound) string {
			return item.ID
		})
	result.Settings.DefaultTarget = target
	if err := result.Validate(); err != nil {
		state.drop(KindSettings, "default target", err.Error())
		result.Settings.DefaultTarget = TargetDirect
	}
	state.noteReferences(&result)
	return result, state.items
}

func readProblems(path string) Problems {
	result := Problems{Items: []Problem{}}
	if data, err := os.ReadFile(path); err != nil || json.Unmarshal(data, &result) != nil ||
		result.Items == nil {
		return Problems{Items: []Problem{}}
	}
	return result
}

func (state *repairState) drop(kind string, id string, reason string) {
	state.items = append(state.items, Problem{Kind: kind, ID: id, Reason: reason})
}

// NOTE: A route or an inbound that points to a dropped element stays in the configuration:
// it works again when the element is added back with the same ID.
func (state *repairState) noteReferences(result *Config) {
	missing := func(id string) bool {
		return !result.TargetExists(id) && slices.ContainsFunc(state.items, func(item Problem) bool {
			return item.ID == id && (item.Kind == KindOutbound || item.Kind == KindFallback)
		})
	}
	for _, route := range result.Routes {
		if missing(route.Target) {
			state.items = append(state.items, Problem{Kind: KindRoute, ID: route.ID, Kept: true,
				Reason: fmt.Sprintf("target '%v' does not exist", route.Target)})
		}
	}
	for _, inbound := range result.Inbounds {
		if missing(inbound.Outbound) {
			state.items = append(state.items, Problem{Kind: KindInbound, ID: inbound.ID, Kept: true,
				Reason: fmt.Sprintf("outbound '%v' does not exist", inbound.Outbound)})
		}
	}
}

func (store *Store) Problems() Problems {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	result := store.problems
	result.Items = append([]Problem{}, store.problems.Items...)
	return result
}

func (store *Store) ClearProblems() error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	path := filepath.Join(filepath.Dir(store.path), problemsName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove file '%v': %w", path, err)
	}
	store.problems = Problems{Items: []Problem{}}
	return nil
}

func (store *Store) load(data []byte, cause error) error {
	directory := filepath.Dir(store.path)
	problems := readProblems(filepath.Join(directory, problemsName))
	if cause == nil {
		store.problems = problems
		return nil
	}
	config, items := repair(data)
	if len(items) == 0 {
		items = []Problem{{Kind: KindFile, ID: fileName, Reason: cause.Error()}}
	}
	if config.Secret == "" {
		secret, err := makeSecret()
		if err != nil {
			return err
		}
		config.Secret = secret
	}
	now := time.Now()
	problems.Time, problems.Backup = now.Unix(), fmt.Sprintf(backupPattern, now.Unix())
	problems.Items = append(problems.Items, items...)
	report, err := json.MarshalIndent(problems, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode problems: %w", err)
	} else if err = WriteFile(filepath.Join(directory, problems.Backup), data); err != nil {
		return err
	} else if err = store.save(config); err != nil {
		return err
	} else if err = WriteFile(filepath.Join(directory, problemsName), report); err != nil {
		return err
	}
	store.config, store.problems = config, problems
	return nil
}
