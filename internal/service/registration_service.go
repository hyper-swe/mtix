// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Take-over limits (TM I9, ADR-007 §3.4): one notice per displaced session per
// noticeWindow, and at most takeOverLimit take-overs per id per takeOverWindow
// before a terminal is required.
const (
	noticeWindow    = 10 * time.Minute
	takeOverWindow  = time.Hour
	takeOverLimit   = 3
	maxHandleLength = 256
)

// Registration outcomes reported to the caller.
const (
	OutcomeRegistered   = "registered"
	OutcomeRefreshed    = "refreshed"
	OutcomeReplacedDead = "replaced_dead"
	OutcomeTakenOver    = "taken_over"
)

var (
	// ErrTakeOverTTYRequired means the per-id take-over limit was reached and
	// only a terminal may take the id over (TM I9).
	ErrTakeOverTTYRequired = errors.New("take-over limit reached for this id; run it from a terminal")
	// ErrNoNoticeRoute means the displaced session cannot be told (no work
	// item to carry the notice). The take-over still succeeds.
	ErrNoNoticeRoute = errors.New("no route for the take-over notice")

	harnessPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
)

// RegistrationStore is the board-local persistence a RegistrationService needs.
type RegistrationStore interface {
	RegisterSessionAtomic(ctx context.Context, key sqlite.RegistrationKey,
		decide func(existing *sqlite.Registration, journalMax int64) (*sqlite.Registration, error)) (*sqlite.Registration, error)
	ListRegistrations(ctx context.Context, hostID, uid string) ([]sqlite.Registration, error)
	DeleteRegistrationIf(ctx context.Context, key sqlite.RegistrationKey, seen sqlite.RegistrationSnapshot) (int64, error)
}

// TakeOverRecord is one operator-local audit entry for a take-over.
type TakeOverRecord struct {
	AgentID          string    `json:"agent_id"`
	At               time.Time `json:"at"`
	DisplacedSession string    `json:"displaced_session"`
	NoticeAttempted  bool      `json:"notice_attempted"`
}

// TakeOverLog is the operator-local audit and rate-limit record (never board state).
type TakeOverLog interface {
	Recent(ctx context.Context, agentID string, since time.Time) ([]TakeOverRecord, error)
	Append(ctx context.Context, rec TakeOverRecord) error
}

// NoticeSender delivers the one fixed take-over notice through the inbox channel.
type NoticeSender interface {
	SendTakeOverNotice(ctx context.Context, agentID string) error
}

// RegistrationHost identifies this host, user and binary in every row.
type RegistrationHost struct {
	HostID   string
	UID      string
	Version  string
	Protocol int
}

// RegistrationDeps are the injected collaborators of RegistrationService.
type RegistrationDeps struct {
	Store     RegistrationStore
	Liveness  Liveness
	TakeOvers TakeOverLog
	Notices   NoticeSender
	Logger    *slog.Logger
	Clock     func() time.Time
	Host      RegistrationHost
}

// RegistrationService owns session registrations (ADR-007 §3, MTIX-112.17.4).
type RegistrationService struct{ RegistrationDeps }

// NewRegistrationService wires a RegistrationService; Logger defaults to slog.Default.
func NewRegistrationService(d RegistrationDeps) *RegistrationService {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &RegistrationService{d}
}

// RegisterSessionRequest describes the calling session.
type RegisterSessionRequest struct {
	AgentID        string
	Harness        string
	SessionHandle  string
	Origin         string // session-env | manual | mcp
	PermissionMode string
	PID            int
	PIDStartTime   string
	TakeOver       bool
	HasTTY         bool
}

// RegisterSessionResult reports what registration did.
type RegisterSessionResult struct {
	Registration *sqlite.Registration
	Outcome      string
	NoticeSent   bool
}

// SessionIdentity is the caller's own session, used to recognize "mine".
type SessionIdentity struct {
	Handle       string
	PID          int
	PIDStartTime string
}

func (s SessionIdentity) matches(r *sqlite.Registration) bool {
	if s.Handle != "" && r.SessionHandle != "" {
		return s.Handle == r.SessionHandle // two handles decide alone: one process can host several sessions
	}
	if s.PID <= 0 || int64(s.PID) != r.PID || s.PIDStartTime == "" || r.PIDStartTime == "" {
		// No start time on either side (an unsure walk recorded a short-lived
		// parent): the same pid proves nothing, a reused pid is another session.
		return false
	}
	return strings.TrimPrefix(s.PIDStartTime, UnverifiedStartPrefix) == strings.TrimPrefix(r.PIDStartTime, UnverifiedStartPrefix)
}

func (r RegisterSessionRequest) identity() SessionIdentity {
	return SessionIdentity{Handle: r.SessionHandle, PID: r.PID, PIDStartTime: r.PIDStartTime}
}

func validateRegisterRequest(r *RegisterSessionRequest) error {
	if r.AgentID == "" {
		return fmt.Errorf("agent ID is required: %w", model.ErrInvalidInput)
	}
	if err := validateAgentID(r.AgentID); err != nil {
		return err
	}
	switch r.Origin {
	case "session-env", "manual", "mcp":
	default:
		return fmt.Errorf("origin %q must be session-env, manual or mcp: %w", r.Origin, model.ErrInvalidInput)
	}
	if r.Harness == "" {
		r.Harness = "unknown"
	}
	if !harnessPattern.MatchString(r.Harness) {
		return fmt.Errorf("harness %q must be lower-case letters, digits, '.', '_' or '-': %w", r.Harness, model.ErrInvalidInput)
	}
	if len(r.SessionHandle) > maxHandleLength {
		return fmt.Errorf("session handle exceeds %d bytes: %w", maxHandleLength, model.ErrInvalidInput)
	}
	for _, c := range r.SessionHandle {
		if unicode.IsControl(c) {
			return fmt.Errorf("session handle contains a control character: %w", model.ErrInvalidInput)
		}
	}
	if r.Origin == "mcp" { // I30: an MCP origin has no handle and no live route
		r.SessionHandle, r.PID, r.PIDStartTime = "", 0, ""
	}
	return nil
}

func (s *RegistrationService) key(agentID string) sqlite.RegistrationKey {
	return sqlite.RegistrationKey{AgentID: agentID, HostID: s.Host.HostID, UID: s.Host.UID}
}

func (s *RegistrationService) newRow(r RegisterSessionRequest, epoch, highWater int64, registeredAt string) *sqlite.Registration {
	now := s.Clock().UTC().Format(time.RFC3339)
	if registeredAt == "" {
		registeredAt = now
	}
	return &sqlite.Registration{AgentID: r.AgentID, HostID: s.Host.HostID, UID: s.Host.UID, Harness: r.Harness,
		SessionHandle: r.SessionHandle, PID: int64(r.PID), PIDStartTime: r.PIDStartTime, PermissionMode: r.PermissionMode,
		Origin: r.Origin, Epoch: epoch, HighWater: highWater, RegisteredAt: registeredAt, LastSeen: now,
		RegistrarVersion: s.Host.Version, AttachProtocol: int64(s.Host.Protocol)}
}

// hasNoRoute reports a row that holds nothing live to protect: an MCP origin or
// a row with neither handle nor pid (no live route, TM I30).
func hasNoRoute(r *sqlite.Registration) bool {
	return r.Origin == "mcp" || (r.SessionHandle == "" && r.PID == 0)
}

// RegisterSession records the calling session for an agent id (ADR-007 §3.2-§3.4,
// I9, I30, A3). A dead or route-less row is replaced in the same transaction;
// a live row needs TakeOver and is limited by the operator-local take-over record; the initial
// high-water is the journal maximum, so registering never wakes on backlog.
func (s *RegistrationService) RegisterSession(ctx context.Context, req RegisterSessionRequest) (*RegisterSessionResult, error) {
	if err := validateRegisterRequest(&req); err != nil {
		return nil, err
	}
	res := &RegisterSessionResult{}
	var displaced string
	noticeDue := false
	row, err := s.Store.RegisterSessionAtomic(ctx, s.key(req.AgentID), func(existing *sqlite.Registration, tail int64) (*sqlite.Registration, error) {
		switch {
		case existing == nil:
			res.Outcome = OutcomeRegistered
			return s.newRow(req, 1, tail, ""), nil
		case req.identity().matches(existing):
			res.Outcome = OutcomeRefreshed
			next := *existing
			next.LastSeen = s.Clock().UTC().Format(time.RFC3339)
			next.Harness, next.PermissionMode, next.Origin = req.Harness, req.PermissionMode, req.Origin
			// A resumed session keeps its handle in a new process: the row must
			// follow it, or a probe sees the old, dead pid and replaces a live lane.
			next.PID, next.PIDStartTime = int64(req.PID), req.PIDStartTime
			return &next, nil
		case hasNoRoute(existing) || s.Liveness.Probe(ctx, *existing) == LiveDead:
			res.Outcome = OutcomeReplacedDead
			return s.newRow(req, existing.Epoch+1, tail, ""), nil
		}
		var err error
		displaced = displacedKey(existing)
		noticeDue, err = s.authoriseTakeOver(ctx, req, displaced)
		if err != nil {
			return nil, err
		}
		res.Outcome = OutcomeTakenOver
		return s.newRow(req, existing.Epoch+1, tail, ""), nil
	})
	if err != nil {
		return nil, err
	}
	res.Registration = row
	if res.Outcome == OutcomeTakenOver {
		res.NoticeSent = s.sendNotice(ctx, req.AgentID, noticeDue)
	}
	return res, nil
}

func displacedKey(r *sqlite.Registration) string {
	if r.SessionHandle != "" {
		return r.SessionHandle
	}
	return fmt.Sprintf("pid:%d:%s", r.PID, r.PIDStartTime)
}

// authoriseTakeOver enforces I9 for a live row and writes the operator-local
// audit entry before the registration commits. It reports whether a notice is
// due for the displaced session.
func (s *RegistrationService) authoriseTakeOver(ctx context.Context, req RegisterSessionRequest, displaced string) (bool, error) {
	if !req.TakeOver {
		return false, fmt.Errorf("agent %q has a live or unverifiable session on this host; re-run with --take-over to replace it: %w",
			req.AgentID, model.ErrAgentStillActive)
	}
	now := s.Clock().UTC()
	recent, err := s.TakeOvers.Recent(ctx, req.AgentID, now.Add(-takeOverWindow))
	if err != nil {
		return false, fmt.Errorf("read take-over record: %w", err)
	}
	if len(recent) >= takeOverLimit && !req.HasTTY {
		return false, fmt.Errorf("agent %q: %w", req.AgentID, ErrTakeOverTTYRequired)
	}
	due := true
	for _, r := range recent {
		if r.DisplacedSession == displaced && r.NoticeAttempted && !r.At.Before(now.Add(-noticeWindow)) {
			due = false
		}
	}
	rec := TakeOverRecord{AgentID: req.AgentID, At: now, DisplacedSession: displaced, NoticeAttempted: due}
	if err := s.TakeOvers.Append(ctx, rec); err != nil {
		return false, fmt.Errorf("write take-over audit: %w", err)
	}
	return due, nil
}

// sendNotice delivers the fixed notice after the take-over committed. A failure
// never undoes the take-over; it is logged and reported as not sent.
func (s *RegistrationService) sendNotice(ctx context.Context, agentID string, due bool) bool {
	if !due {
		return false
	}
	if err := s.Notices.SendTakeOverNotice(ctx, agentID); err != nil {
		if !errors.Is(err, ErrNoNoticeRoute) {
			s.Logger.Warn("take-over notice not delivered", slog.String("agent_id", agentID), slog.Any("error", err))
		}
		return false
	}
	return true
}
