// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

const (
	claudeHookErrorMax    = 300 // bounded text kept per record
	claudeHookRecordLimit = 256 // a corrupt or hostile directory cannot flood doctor
)

var claudeHookAgentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ClaudeHookFailure is one hook's latest unresolved failure.
type ClaudeHookFailure struct {
	Agent string          `json:"agent"`
	Event ClaudeHookEvent `json:"event"`
	At    time.Time       `json:"at"`
	Error string          `json:"error"`
}

// ClaudeHookLog keeps the latest failure of each (agent, event) as one small
// file, so concurrent hooks never share a file. A later successful run of the
// same hook removes its record: doctor shows only what is still broken.
type ClaudeHookLog struct {
	Dir   string
	Clock func() time.Time
}

func (l *ClaudeHookLog) path(agent string, ev ClaudeHookEvent) (string, error) {
	if !claudeHookAgentPattern.MatchString(agent) {
		return "", fmt.Errorf("hook agent id %q is not a safe file name: %w", agent, model.ErrInvalidInput)
	}
	return filepath.Join(l.Dir, agent+"."+string(ev)+".json"), nil
}

// Record stores err as the latest failure of the hook, or clears the record
// when err is nil. A nil log is a no-op.
func (l *ClaudeHookLog) Record(agent string, ev ClaudeHookEvent, err error) error {
	if l == nil {
		return nil
	}
	p, perr := l.path(agent, ev)
	if perr != nil {
		return perr
	}
	if err == nil {
		if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("clear hook record: %w", rerr)
		}
		return nil
	}
	text := err.Error()
	if len(text) > claudeHookErrorMax {
		text = text[:claudeHookErrorMax]
	}
	data, merr := json.Marshal(ClaudeHookFailure{Agent: agent, Event: ev, At: l.Clock().UTC(), Error: text})
	if merr != nil {
		return fmt.Errorf("encode hook record: %w", merr)
	}
	if derr := os.MkdirAll(l.Dir, 0o700); derr != nil {
		return fmt.Errorf("create hook record dir: %w", derr)
	}
	tmp := p + ".tmp"
	if werr := os.WriteFile(tmp, data, 0o600); werr != nil {
		return fmt.Errorf("write hook record: %w", werr)
	}
	if rerr := os.Rename(tmp, p); rerr != nil {
		return fmt.Errorf("publish hook record: %w", rerr)
	}
	return nil
}

// Failures lists the unresolved failures, oldest first by file name.
func (l *ClaudeHookLog) Failures() ([]ClaudeHookFailure, error) {
	entries, err := os.ReadDir(l.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hook record dir: %w", err)
	}
	var out []ClaudeHookFailure
	for _, e := range entries {
		if len(out) >= claudeHookRecordLimit || e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(l.Dir, e.Name())) //nolint:gosec // G304: a file name listed from the hook record dir
		if rerr != nil {
			return nil, fmt.Errorf("read hook record %s: %w", e.Name(), rerr)
		}
		var f ClaudeHookFailure
		if jerr := json.Unmarshal(data, &f); jerr != nil {
			return nil, fmt.Errorf("decode hook record %s: %w", e.Name(), jerr)
		}
		out = append(out, f)
	}
	return out, nil
}

// ClaudeHookDoctorCheck is the wake.listener.hook-errors row (MTIX-112.40.4):
// a hook that failed is visible here with the exact command that re-runs it.
// A successful re-run clears the record, so the row turns green by itself.
func ClaudeHookDoctorCheck(log *ClaudeHookLog) DoctorCheck {
	return DoctorCheck{ID: "wake.listener.hook-errors", Run: func(context.Context) DoctorResult {
		failures, err := log.Failures()
		if err != nil {
			return DoctorResult{Status: DoctorFail, Detail: "cannot read the Claude hook error records: " + err.Error(),
				Fix: "mtix doctor --json"}
		}
		if len(failures) == 0 {
			return DoctorResult{Status: DoctorInfo, Detail: "no Claude session hook failures recorded", Fix: "mtix doctor --json"}
		}
		first := failures[0]
		return DoctorResult{
			Status: DoctorFail,
			Detail: fmt.Sprintf("%d Claude session hook failure(s); first: %s %s at %s: %s",
				len(failures), first.Agent, first.Event, first.At.Format(time.RFC3339), first.Error),
			Fix:  fmt.Sprintf("mtix hook claude-session %s --agent %s", first.Event, first.Agent),
			Data: map[string]any{"failures": failures},
		}
	}}
}
