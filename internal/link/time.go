package link

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

var faketimePatterns = []string{"/usr/lib/*/faketime/libfaketime.so.1",
	"/usr/lib/faketime/libfaketime.so.1"}

func findFaketime() string {
	for _, pattern := range faketimePatterns {
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) != 0 {
			return matches[0]
		}
	}
	return ""
}

func (manager *Manager) synchronize(ctx context.Context,
	logf func(format string, arguments ...any)) ([]string, error) {
	timeClock := manager.options.Clock
	if timeClock == nil {
		return nil, nil
	}
	if status := timeClock.Status(); !status.Synchronized() {
		logf("waiting for time synchronization, at most %v", timeClock.WaitLimit())
	}
	status, err := timeClock.Wait(ctx)
	if err != nil && ctx.Err() != nil {
		return nil, err
	} else if err != nil {
		return nil, &fatalError{&clockError{err}}
	}
	if offset := timeClock.Offset(); offset == 0 {
		logf("time: %v", status.Message)
		return nil, nil
	} else if manager.options.Tools.Faketime == "" {
		return nil, &fatalError{fmt.Errorf("host clock is %v off and libfaketime is not installed",
			offset.Round(time.Millisecond))}
	} else {
		logf("time: %v; OpenVPN runs with offset %v", status.Message,
			offset.Round(time.Millisecond))
		return []string{"LD_PRELOAD=" + manager.options.Tools.Faketime,
			fmt.Sprintf("FAKETIME=%+.3f", offset.Seconds()), "FAKETIME_DONT_FAKE_MONOTONIC=1"}, nil
	}
}
