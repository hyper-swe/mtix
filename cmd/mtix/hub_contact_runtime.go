// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
)

// hubActivityDebounce bounds how often an MCP server rewrites the activity
// record while it commits continuously.
const hubActivityDebounce = time.Minute

// utcNow is the production clock handed to the hub-contact code at its
// wiring points; everything below takes the clock as a parameter.
func utcNow() time.Time { return time.Now().UTC() }

// hubContactSource reads the scheduler inputs for one board (MTIX-112.20.1):
// the board's config, the operator-local activity record and the live
// listener leases. Nothing here touches the board's database or tasks.json.
type hubContactSource struct {
	mtixDir             string
	state               *oplocal.State
	startTime           func(pid int) (string, error)
	now                 func() time.Time
	grandfatherInterval time.Duration // the daemon's own --interval
}

func newHubContactSource(mtixDir string) (*hubContactSource, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, fmt.Errorf("hub contact: %w", err)
	}
	canonical, err := canonicalBoardDir(mtixDir)
	if err != nil {
		return nil, err
	}
	return &hubContactSource{mtixDir: canonical, state: oplocal.New(env), startTime: procinfo.StartTime,
		now: utcNow, grandfatherInterval: service.GrandfatherInterval}, nil
}

// canonicalBoardDir resolves every spelling of one board (relative, symlinked,
// /var versus /private/var) to a single absolute path, so the activity writer
// and the daemon's reader always derive the same per-board key.
func canonicalBoardDir(mtixDir string) (string, error) {
	abs, err := filepath.Abs(mtixDir)
	if err != nil {
		return "", fmt.Errorf("hub contact: resolve board path %q: %w", mtixDir, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("hub contact: resolve board path %q: %w", mtixDir, err)
	}
	return resolved, nil
}

func (s *hubContactSource) configPath() string { return filepath.Join(s.mtixDir, "config.yaml") }

func (s *hubContactSource) key() string { return oplocal.BoardKey(s.mtixDir) }

// snapshot is the daemon's per-pass input load. Unreadable input returns an
// error, which the scheduler treats as "do not contact".
func (s *hubContactSource) snapshot() (service.ContactInputs, error) {
	var in service.ContactInputs
	cfg, err := service.NewConfigService(s.configPath())
	if err != nil {
		return in, fmt.Errorf("%w: %w", service.ErrContactInputs, err)
	}
	if in.Mode, _, err = cfg.HubContact(s.grandfatherInterval); err != nil {
		return in, fmt.Errorf("%w: %w", service.ErrContactInputs, err)
	}
	if in.Listen, err = cfg.ListenSettings(); err != nil {
		return in, fmt.Errorf("%w: %w", service.ErrContactInputs, err)
	}
	act, present, err := s.state.ReadHubActivity(s.key())
	if err != nil {
		return in, fmt.Errorf("%w: activity: %w", service.ErrContactInputs, err)
	}
	if present {
		in.LastActivity = act.At
	}
	if in.LiveLease, err = s.liveLeases(); err != nil {
		return in, fmt.Errorf("%w: leases: %w", service.ErrContactInputs, err)
	}
	return in, nil
}

// liveLeases reports whether any lease names a still-running process: the
// same pid and the same start time, so a reused pid is not live.
func (s *hubContactSource) liveLeases() (bool, error) {
	leases, err := s.state.ListListenLeases(s.key())
	if err != nil {
		return false, fmt.Errorf("list leases: %w", err)
	}
	for _, l := range leases {
		if got, startErr := s.startTime(l.PID); startErr == nil && got == l.StartTime {
			return true, nil
		}
	}
	return false, nil
}

// recordContact stores the outcome of one scheduled contact for doctor.
func (s *hubContactSource) recordContact(at time.Time, route string) error {
	if err := s.state.RecordHubContact(s.key(), oplocal.HubContact{At: at, Route: route, OK: true}); err != nil {
		return fmt.Errorf("record hub contact: %w", err)
	}
	return nil
}

// recordHubActivityAt notes real activity on one board at the injected time.
// Best effort: a refused operator state must never fail the command that
// caused it, so the error is only returned for the caller to log.
func recordHubActivityAt(mtixDir string, kind oplocal.ActivityKind, now func() time.Time) error {
	if mtixDir == "" {
		return nil
	}
	src, err := newHubContactSource(mtixDir)
	if err != nil {
		return err
	}
	if err := src.state.RecordHubActivity(src.key(), oplocal.HubActivity{At: now(), Kind: kind}); err != nil {
		return fmt.Errorf("record hub activity: %w", err)
	}
	return nil
}

// recordHubActivity records activity on the current board with the production clock.
func recordHubActivity(kind oplocal.ActivityKind) {
	if err := recordHubActivityAt(app.mtixDir, kind, utcNow); err != nil && app.logger != nil {
		app.logger.Debug("hub activity not recorded", "error", err)
	}
}

// commandActivity classifies a finished CLI command. Mutation commands are
// activity (a comment with --to is an addressed send); so is an agent
// heartbeat, which a model turn or a person runs. Reads, the inbox and every
// wait are not.
func commandActivity(cmd *cobra.Command) (oplocal.ActivityKind, bool) {
	switch {
	case isMutationCommand(cmd.Name()):
		if cmd.Name() == "comment" {
			if f := cmd.Flags().Lookup("to"); f != nil && f.Value.String() != "" {
				return oplocal.ActivityAddressedSend, true
			}
		}
		return oplocal.ActivityMutation, true
	case cmd.Name() == "heartbeat" && cmd.Parent() != nil && cmd.Parent().Name() == "agent":
		return oplocal.ActivityTurn, true
	}
	return "", false
}

// attachVerbActivity classifies an attach glue verb: a session start and a
// prompt submit are activity; session end and the wait verb are not.
func attachVerbActivity(verb string) (oplocal.ActivityKind, bool) {
	switch verb {
	case attach.VerbSessionStart, attach.VerbPromptSubmit:
		return oplocal.ActivityGlue, true
	}
	return "", false
}

// recordGlueActivity records glue activity for the board found from the
// working directory; no board means nothing to record.
func recordGlueActivity(verb string) {
	kind, ok := attachVerbActivity(verb)
	if !ok {
		return
	}
	dir, err := findMtixDir()
	if err != nil {
		return
	}
	// Best effort: glue must never fail, so the error is only logged.
	if err := recordHubActivityAt(dir, kind, utcNow); err != nil && app.logger != nil {
		app.logger.Debug("hub glue activity not recorded", "error", err)
	}
}

// activityDebouncer rate-limits an activity writer with an injected clock.
type activityDebouncer struct {
	mu       sync.Mutex
	now      func() time.Time
	interval time.Duration
	last     time.Time
	record   func()
}

// Trigger runs record unless it ran less than interval ago.
func (d *activityDebouncer) Trigger() {
	d.mu.Lock()
	now := d.now()
	due := d.last.IsZero() || now.Sub(d.last) >= d.interval
	if due {
		d.last = now
	}
	d.mu.Unlock()
	if due {
		d.record()
	}
}

// wireHubActivity records every MCP-server mutation commit as hub activity
// (MTIX-112.20.1), debounced so a busy agent does not rewrite the operator
// record on every commit. An MCP wait commits nothing, so it records nothing.
// Returns a no-op cleanup.
func wireHubActivity() (cleanup func()) {
	if app.store == nil || app.mtixDir == "" {
		return func() {}
	}
	deb := &activityDebouncer{now: utcNow, interval: hubActivityDebounce,
		record: func() { recordHubActivity(oplocal.ActivityMutation) }}
	app.store.AddOnCommit(deb.Trigger)
	return func() {}
}
