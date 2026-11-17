package link

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"proximity/internal/config"
)

type ServiceRecord struct {
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	Since     int64  `json:"since"`
	Signature string `json:"signature,omitempty"`
}

type journal struct {
	mutex   sync.Mutex
	path    string
	records map[string]ServiceRecord
}

type logBook struct {
	mutex   sync.Mutex
	path    string
	lines   []string
	written int
	limit   int
}

const (
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
)

const intentRunning = "running"
const journalName = "services.json"
const logsDirectory = "logs"
const logSuffix = ".log"
const outboundPrefix = "outbound-"
const inboundPrefix = "inbound-"
const messageStopped = "stopped by user"
const messageAutostart = "not started automatically: autostart is off"
const messageInactive = "router is stopped"

func outboundKey(slot int) string {
	return outboundPrefix + strconv.Itoa(slot)
}

func inboundKey(port int) string {
	return inboundPrefix + strconv.Itoa(port)
}

func digest(signature string) string {
	sum := sha256.Sum256([]byte(signature))
	return hex.EncodeToString(sum[:16])
}

func stamp(format string, arguments ...any) string {
	return time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, arguments...)
}

func openJournal(directory string) (*journal, error) {
	result := &journal{records: map[string]ServiceRecord{}}
	if directory == "" {
		return result, nil
	} else if err := os.MkdirAll(filepath.Join(directory, logsDirectory), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create directory '%v': %w", directory, err)
	}
	result.path = filepath.Join(directory, journalName)
	if data, err := os.ReadFile(result.path); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to read file '%v': %w", result.path, err)
	} else if err = json.Unmarshal(data, &result.records); err != nil || result.records == nil {
		// NOTE: Broken journal must not stop the router: services start as if it were empty.
		result.records = map[string]ServiceRecord{}
	}
	return result, nil
}

func openLogBook(path string) *logBook {
	result := &logBook{path: path, lines: []string{}, limit: maxLogLines}
	if data, err := os.ReadFile(path); err == nil && len(data) != 0 && path != "" {
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		result.written = len(lines)
		result.lines = lines[max(0, len(lines)-maxLogLines):]
	}
	return result
}

func (journal *journal) get(key string) (ServiceRecord, bool) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	record, found := journal.records[key]
	return record, found
}

func (journal *journal) set(key string, record ServiceRecord) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	if current, found := journal.records[key]; found && current.State == record.State &&
		current.Message == record.Message && current.Signature == record.Signature {
		return
	}
	journal.records[key] = record
	journal.save()
}

func (journal *journal) remove(keep func(key string) bool) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	changed := false
	for key := range journal.records {
		if !keep(key) {
			delete(journal.records, key)
			changed = true
		}
	}
	if changed {
		journal.save()
	}
}

func (journal *journal) save() {
	if journal.path == "" {
		return
	} else if data, err := json.MarshalIndent(journal.records, "", "  "); err == nil {
		_ = config.WriteFile(journal.path, data)
	}
}

func (book *logBook) add(line string) {
	book.mutex.Lock()
	defer book.mutex.Unlock()
	if book.lines = append(book.lines, line); len(book.lines) > book.limit {
		book.lines = book.lines[len(book.lines)-book.limit:]
	}
	if book.path == "" {
		return
	} else if book.written >= 2*book.limit {
		book.written = len(book.lines)
		_ = config.WriteFile(book.path, []byte(strings.Join(book.lines, "\n")+"\n"))
		return
	}
	file, err := os.OpenFile(book.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	if _, err = file.WriteString(line + "\n"); err == nil {
		book.written++
	}
}

func (book *logBook) resize(verbose bool) {
	book.mutex.Lock()
	defer book.mutex.Unlock()
	if book.limit = maxLogLines; verbose {
		book.limit = maxVerboseLines
	}
}

func (book *logBook) snapshot() []string {
	book.mutex.Lock()
	defer book.mutex.Unlock()
	return append([]string{}, book.lines...)
}
