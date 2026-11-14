package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Listener func()

type Store struct {
	mutex     sync.Mutex
	path      string
	config    Config
	listeners []Listener
}

const fileName = "proximity.json"

func Open(directory string) (*Store, error) {
	result, err := internalOpen(directory)
	if err != nil {
		return nil, fmt.Errorf("config.Open failed: %w", err)
	}
	return result, nil
}

func WriteFile(path string, data []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("failed to write file '%v': %w", temporary, err)
	} else if err = os.Rename(temporary, path); err != nil {
		return fmt.Errorf("failed to rename file '%v': %w", temporary, err)
	}
	return nil
}

func internalOpen(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create directory '%v': %w", directory, err)
	}
	store := &Store{path: filepath.Join(directory, fileName), config: Default()}
	if data, err := os.ReadFile(store.path); errors.Is(err, os.ErrNotExist) {
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return nil, fmt.Errorf("failed to read random data: %w", err)
		}
		store.config.Secret = base64.StdEncoding.EncodeToString(secret)
		if err = store.save(store.config); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("failed to read file '%v': %w", store.path, err)
	} else if err = json.Unmarshal(data, &store.config); err != nil {
		return nil, fmt.Errorf("failed to decode file '%v': %w", store.path, err)
	} else if err = store.config.Validate(); err != nil {
		return nil, fmt.Errorf("failed to check file '%v': %w", store.path, err)
	}
	return store, nil
}

func (store *Store) Snapshot() Config {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.config.Clone()
}

func (store *Store) Subscribe(listener Listener) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.listeners = append(store.listeners, listener)
}

func (store *Store) Update(mutate func(config *Config) error) error {
	listeners, err := store.update(mutate)
	if err != nil {
		return err
	}
	for _, listener := range listeners {
		listener()
	}
	return nil
}

func (store *Store) update(mutate func(config *Config) error) ([]Listener, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	config := store.config.Clone()
	if err := mutate(&config); err != nil {
		return nil, err
	} else if err = config.Validate(); err != nil {
		return nil, err
	} else if err = store.save(config); err != nil {
		return nil, err
	}
	store.config = config
	return append([]Listener{}, store.listeners...), nil
}

func (store *Store) save(config Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode configuration: %w", err)
	}
	return WriteFile(store.path, data)
}
