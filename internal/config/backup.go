package config

import (
	"fmt"
	"time"
)

type Backup struct {
	Format    string     `json:"format"`
	Version   int        `json:"version"`
	Created   string     `json:"created"`
	Settings  Settings   `json:"settings"`
	Endpoints []Endpoint `json:"endpoints"`
	Fallbacks []Fallback `json:"fallbacks"`
	Filters   []Filter   `json:"filters"`
}

const BackupFormat = "proximity-backup"

func (config *Config) Export(created time.Time) Backup {
	clone := config.Clone()
	return Backup{Format: BackupFormat, Version: clone.Version,
		Created: created.UTC().Format(time.RFC3339), Settings: clone.Settings,
		Endpoints: clone.Endpoints, Fallbacks: clone.Fallbacks, Filters: clone.Filters}
}

func (config *Config) Import(backup Backup, settings bool) error {
	if backup.Format != BackupFormat {
		return fmt.Errorf("unexpected backup format '%v'", backup.Format)
	} else if backup.Version < 1 || backup.Version > currentVersion {
		return fmt.Errorf("unexpected backup version (%v)", backup.Version)
	}
	source := Config{Version: backup.Version, Settings: backup.Settings,
		Endpoints: backup.Endpoints, Fallbacks: backup.Fallbacks, Filters: backup.Filters}
	source = source.Clone()
	target := source.Settings.DefaultTarget
	if !settings {
		source.Settings = config.Settings
		source.Settings.DefaultTarget = target
	}
	for index := range source.Filters {
		if source.Filters[index].Key != "" {
			continue
		} else if key, err := makeKey(); err != nil {
			return err
		} else {
			source.Filters[index].Key = key
		}
	}
	if source.migrate(); source.Endpoints == nil {
		source.Endpoints = []Endpoint{}
	}
	if source.Fallbacks == nil {
		source.Fallbacks = []Fallback{}
	}
	if source.Filters == nil {
		source.Filters = []Filter{}
	}
	config.Settings, config.Endpoints = source.Settings, source.Endpoints
	config.Fallbacks, config.Filters = source.Fallbacks, source.Filters
	return nil
}
