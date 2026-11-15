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
	Outbounds []Outbound `json:"outbounds"`
	Fallbacks []Fallback `json:"fallbacks"`
	Routes    []Route    `json:"routes"`
	Legacy
}

const BackupFormat = "proximity-backup"

func (config *Config) Export(created time.Time) Backup {
	clone := config.Clone()
	return Backup{Format: BackupFormat, Version: clone.Version,
		Created: created.UTC().Format(time.RFC3339), Settings: clone.Settings,
		Outbounds: clone.Outbounds, Fallbacks: clone.Fallbacks, Routes: clone.Routes}
}

func (config *Config) Import(backup Backup, settings bool) error {
	if backup.Format != BackupFormat {
		return fmt.Errorf("unexpected backup format '%v'", backup.Format)
	} else if backup.Version < 1 || backup.Version > currentVersion {
		return fmt.Errorf("unexpected backup version (%v)", backup.Version)
	}
	source := Config{Version: backup.Version, Settings: backup.Settings,
		Outbounds: backup.Outbounds, Fallbacks: backup.Fallbacks, Routes: backup.Routes,
		Legacy: backup.Legacy}
	source.adopt()
	source = source.Clone()
	target := source.Settings.DefaultTarget
	if !settings {
		source.Settings = config.Settings
		source.Settings.DefaultTarget = target
	}
	for index := range source.Routes {
		if source.Routes[index].Key != "" {
			continue
		} else if key, err := makeKey(); err != nil {
			return err
		} else {
			source.Routes[index].Key = key
		}
	}
	if source.migrate(); source.Outbounds == nil {
		source.Outbounds = []Outbound{}
	}
	if source.Fallbacks == nil {
		source.Fallbacks = []Fallback{}
	}
	if source.Routes == nil {
		source.Routes = []Route{}
	}
	config.Settings, config.Outbounds = source.Settings, source.Outbounds
	config.Fallbacks, config.Routes = source.Fallbacks, source.Routes
	return nil
}
