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

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// attachProtocol is the registration record's attach protocol version.
const attachProtocol = 1

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

// detectedSession is the harness and handle read from flags and environment.
type detectedSession struct {
	Harness string
	Handle  string
	Origin  string
}

// detectSession resolves the harness and session handle. A typed --session is
// origin manual; a handle found in the environment is session-env (ADR-007 §3.3).
func detectSession(getenv func(string) string, opts sessionOptions) detectedSession {
	harness, handle := "unknown", ""
	switch {
	case getenv("CLAUDE_CODE_MESSAGING_SOCKET") != "":
		harness, handle = "claude-code", getenv("CLAUDE_CODE_MESSAGING_SOCKET")
	case getenv("CODEX_THREAD_ID") != "":
		harness, handle = "codex", getenv("CODEX_THREAD_ID")
	case getenv("PI_SESSION_ID") != "":
		harness, handle = "pi", getenv("PI_SESSION_ID")
	}
	origin := "manual"
	if handle != "" {
		origin = "session-env"
	}
	if opts.Harness != "" {
		harness = opts.Harness
	}
	if opts.Session != "" {
		handle, origin = opts.Session, "manual"
	}
	return detectedSession{Harness: harness, Handle: handle, Origin: origin}
}

// callerProcess records the session's long-lived process: the nearest ancestor
// of this command that is not a shell or wrapper (the harness that runs each
// command in a short-lived shell). When the walk is unsure, the start time is
// empty, which probes as unknown and fails safe (never replaced silently).
func callerProcess() (pid int, start string) {
	pid, start, _ = procinfo.FindSessionProcess(procinfo.Info, os.Getppid())
	return pid, start
}

func currentIdentity() service.SessionIdentity {
	pid, start := callerProcess()
	return service.SessionIdentity{Handle: detectSession(os.Getenv, sessionOptions{}).Handle, PID: pid, PIDStartTime: start}
}

// newRegistrationService builds the service for this host and user.
func newRegistrationService() (*service.RegistrationService, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, fmt.Errorf("session identity: %w", err)
	}
	host, err := oplocal.New(env).HostID()
	if err != nil {
		return nil, fmt.Errorf("session host identity: %w", err)
	}
	uid, err := currentUserID()
	if err != nil {
		return nil, fmt.Errorf("session user identity: %w", err)
	}
	takeOvers, err := newOperatorTakeOverLog()
	if err != nil {
		return nil, err
	}
	return service.NewRegistrationService(service.RegistrationDeps{
		Store: app.store, Liveness: service.NewProcessLiveness(procinfo.StartTime), TakeOvers: takeOvers,
		Notices: inboxNoticeSender{}, Logger: app.logger, Clock: func() time.Time { return time.Now().UTC() },
		Host: service.RegistrationHost{HostID: host, UID: uid, Version: version, Protocol: attachProtocol},
	}), nil
}

// sessionContext attaches the caller's session identity so inbox list, wait and
// ack refresh last_seen of the matching registration (ADR-007 §3.3).
func sessionContext(ctx context.Context) context.Context {
	handle := detectSession(os.Getenv, sessionOptions{}).Handle
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
