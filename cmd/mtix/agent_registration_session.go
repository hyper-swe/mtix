// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Session registration wiring for `mtix agent register|unregister|list` (ADR-007 §3, MTIX-112.17.4).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// takeOverNoticeText is the one fixed take-over notice. It carries no host,
// user or agent identifiers (D10).
const takeOverNoticeText = "Another session registered as this agent id with --take-over. " +
	"If that was not you, register again with --take-over to reclaim the id."

// sessionOptions are the typed flags of `mtix agent register`.
type sessionOptions struct {
	Harness  string
	Session  string
	TakeOver bool
}

// detectedSession is the harness and handle for the calling session.
// Uncertain means the harness could not be established from the process that
// owns the session: no handle is recorded and liveness stays unknown.
type detectedSession struct {
	Harness   string
	Handle    string
	Origin    string
	Uncertain bool
}

// harnessEnvVar is the one environment variable each harness owns. A child
// harness inherits its parent's variables, so only the identified harness's
// own variable may supply the handle.
var harnessEnvVar = map[string]string{
	procinfo.HarnessClaude: "CLAUDE_CODE_MESSAGING_SOCKET",
	procinfo.HarnessCodex:  "CODEX_THREAD_ID",
	procinfo.HarnessPi:     "PI_SESSION_ID",
}

// walkedSession is the session process found by the ancestor walk.
type walkedSession = procinfo.SessionProcess

// sessionWalker finds the session process; tests replace it with a process table.
var sessionWalker = func() (walkedSession, bool) {
	return procinfo.FindSession(procinfo.Info, os.Getppid())
}

// detectSession resolves the harness and session handle by identity, never by
// environment order: the harness is the one that owns the walked session
// process, and the handle is read only from that harness's own variable (a
// child harness inherits its parent's variables, so any other variable may
// belong to an outer session). If the walk is unsure, the process is not a
// known harness, or its variable is absent, the session is uncertain and no
// handle is borrowed. A typed --session is origin manual (ADR-007 §3.3).
func detectSession(getenv func(string) string, opts sessionOptions, proc walkedSession, sure bool) detectedSession {
	det := detectedSession{Harness: "unknown", Origin: "manual", Uncertain: true}
	if envVar, ok := harnessEnvVar[proc.Harness]; ok && sure {
		if handle := getenv(envVar); handle != "" {
			det = detectedSession{Harness: proc.Harness, Handle: handle, Origin: "session-env"}
		} else {
			det.Harness = proc.Harness
		}
	}
	if opts.Harness != "" {
		det.Harness = opts.Harness
	}
	if opts.Session != "" {
		det.Handle, det.Origin, det.Uncertain = opts.Session, "manual", false
	}
	return det
}

// currentSession detects the calling session and records its process. An
// uncertain session keeps the pid but no start time, so it probes as unknown.
func currentSession(opts sessionOptions) (det detectedSession, pid int, start string) {
	proc, sure := sessionWalker()
	det = detectSession(os.Getenv, opts, proc, sure)
	switch {
	case !det.Uncertain:
		return det, proc.PID, proc.StartTime
	case sure && proc.StartTime != "":
		// An unidentified but real process: keep its start time marked unverified,
		// so a repeat from the same process refreshes while liveness stays unknown.
		return det, proc.PID, service.UnverifiedStartPrefix + proc.StartTime
	default:
		// An unsure walk recorded the immediate (short-lived) parent: no start time,
		// so nothing can ever treat the same pid as the same session.
		return det, proc.PID, ""
	}
}

func currentIdentity() service.SessionIdentity {
	det, pid, start := currentSession(sessionOptions{})
	return service.SessionIdentity{Handle: det.Handle, PID: pid, PIDStartTime: start}
}

// registrationHostIdentity returns this host's operator-local host id and the
// current user id: the key of registrations and dispatcher heartbeats.
func registrationHostIdentity() (host, uid string, err error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return "", "", fmt.Errorf("session identity: %w", err)
	}
	host, err = oplocal.New(env).HostID()
	if err != nil {
		return "", "", fmt.Errorf("session host identity: %w", err)
	}
	uid, err = currentUserID()
	if err != nil {
		return "", "", fmt.Errorf("session user identity: %w", err)
	}
	return host, uid, nil
}

// newRegistrationService builds the service for this host and user. Sessions
// register at this binary's attach protocol, the version a newer CLI compares
// dispatcher heartbeats against (ADR-007 §6.4).
func newRegistrationService() (*service.RegistrationService, error) {
	return newRegistrationServiceWithRouting(true)
}

// Registration diagnostics do not need attach routing or its store identity lookup.
func newRegistrationServiceWithRouting(includeRouting bool) (*service.RegistrationService, error) {
	host, uid, err := registrationHostIdentity()
	if err != nil {
		return nil, err
	}
	takeOvers, err := newOperatorTakeOverLog()
	if err != nil {
		return nil, err
	}
	var routing *service.AttachRouting
	if includeRouting {
		routing, err = currentRegistrationRouting()
		if err != nil {
			return nil, err
		}
	}
	return service.NewRegistrationService(service.RegistrationDeps{
		Routing: routing,
		Store:   app.store, Liveness: service.NewProcessLiveness(procinfo.StartTime), TakeOvers: takeOvers,
		Notices: inboxNoticeSender{}, Logger: app.logger, Clock: func() time.Time { return time.Now().UTC() },
		Host: service.RegistrationHost{HostID: host, UID: uid, Version: version, Protocol: attach.Protocol},
	}), nil
}

// sessionContext attaches the caller's session identity so inbox list, wait and
// ack refresh last_seen of the matching registration (ADR-007 §3.3).
func sessionContext(ctx context.Context) context.Context {
	det, _, _ := currentSession(sessionOptions{})
	handle := det.Handle
	if handle == "" {
		return ctx
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return ctx
	}
	host, err := oplocal.New(env).HostID()
	if err != nil {
		return ctx
	}
	uid, err := currentUserID()
	if err != nil {
		return ctx
	}
	return sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{HostID: host, UID: uid, Handle: handle})
}

// inboxNoticeSender delivers the take-over notice as an inbox comment on the
// displaced agent's current work item. With no work item there is no route.
type inboxNoticeSender struct{}

// SendTakeOverNotice implements service.NoticeSender through the inbox channel.
func (inboxNoticeSender) SendTakeOverNotice(ctx context.Context, agentID string) error {
	node, err := app.agentSvc.GetCurrentWork(ctx, agentID)
	if err != nil {
		return fmt.Errorf("%w: %w", service.ErrNoNoticeRoute, err)
	}
	if err := app.promptSvc.AddAnnotation(ctx, node.ID, takeOverNoticeText, "mtix", agentID); err != nil {
		return fmt.Errorf("deliver take-over notice: %w", err)
	}
	return nil
}

// operatorTakeOverLog keeps take-over audit entries operator-local (TM I9),
// keyed by board and agent id so limits and notices are per board.
type operatorTakeOverLog struct {
	state *oplocal.State
	board string
}

// storedTakeOver is one operator-local entry with the board it belongs to.
type storedTakeOver struct {
	Board string `json:"board"`
	service.TakeOverRecord
}

// boardKey names the board for operator-local records: its committed board
// identity when there is exactly one, otherwise the absolute store directory.
func boardKey() string {
	if ids, err := service.ReadBoardIDs(app.mtixDir); err == nil && len(ids) == 1 {
		return "board:" + ids[0]
	}
	abs, err := filepath.Abs(app.mtixDir)
	if err != nil {
		abs = app.mtixDir
	}
	return "path:" + abs
}

const takeOverRecordName = "takeovers"

func newOperatorTakeOverLog() (*operatorTakeOverLog, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, fmt.Errorf("take-over log: %w", err)
	}
	return &operatorTakeOverLog{state: oplocal.New(env), board: boardKey()}, nil
}

func (l *operatorTakeOverLog) read() ([]storedTakeOver, error) {
	var recs []storedTakeOver
	if _, err := l.state.ReadJSON(takeOverRecordName, &recs); err != nil {
		return nil, fmt.Errorf("read take-over record: %w", err)
	}
	return recs, nil
}

// Recent implements service.TakeOverLog.
func (l *operatorTakeOverLog) Recent(_ context.Context, agentID string, since time.Time) ([]service.TakeOverRecord, error) {
	recs, err := l.read()
	if err != nil {
		return nil, err
	}
	var out []service.TakeOverRecord
	for _, r := range recs {
		if r.Board == l.board && r.AgentID == agentID && !r.At.Before(since) {
			out = append(out, r.TakeOverRecord)
		}
	}
	return out, nil
}

// Append implements service.TakeOverLog; entries older than a day are dropped.
func (l *operatorTakeOverLog) Append(_ context.Context, rec service.TakeOverRecord) error {
	recs, err := l.read()
	if err != nil {
		return err
	}
	kept := recs[:0]
	for _, r := range recs {
		if r.At.After(rec.At.Add(-24 * time.Hour)) {
			kept = append(kept, r)
		}
	}
	if err := l.state.WriteJSON(takeOverRecordName, append(kept, storedTakeOver{Board: l.board, TakeOverRecord: rec})); err != nil {
		return fmt.Errorf("write take-over record: %w", err)
	}
	return nil
}
