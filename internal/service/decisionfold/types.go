// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold

import "github.com/hyper-swe/mtix/internal/model"

// State is the state of a decision card. The set is closed.
type State string

// Card states.
const (
	StateOpen       State = "open"
	StateConflicted State = "conflicted"
	StateAnswered   State = "answered"
	StateApplied    State = "applied"
	StateWithdrawn  State = "withdrawn"
	StateSuperseded State = "superseded"
	StateInvalid    State = "invalid"
)

// Flag is a fold flag. Per-viewer presentation flags are computed elsewhere.
type Flag string

// Fold flags, in the order a card lists them.
const (
	FlagReopenedAfterRefusal Flag = "reopened_after_refusal"
	FlagTicketDeleted        Flag = "ticket_deleted"
	FlagTicketMatchedByPath  Flag = "ticket_matched_by_path"
	FlagKeyConflict          Flag = "key_conflict"
	FlagDuplicateKeyConflict Flag = "duplicate_key_conflict"
	FlagOutcomeConflict      Flag = "outcome_conflict"
	FlagRedacted             Flag = "redacted"
	FlagUnsupportedEvents    Flag = "unsupported_events"
)

var flagOrder = []Flag{
	FlagReopenedAfterRefusal, FlagTicketDeleted, FlagTicketMatchedByPath, FlagKeyConflict,
	FlagDuplicateKeyConflict, FlagOutcomeConflict, FlagRedacted, FlagUnsupportedEvents,
}

// InvalidReason says why a card is invalid.
type InvalidReason string

// Reasons a card is invalid, in priority order.
const (
	ReasonNone          InvalidReason = ""
	ReasonMalformed     InvalidReason = "malformed"
	ReasonUnsupported   InvalidReason = "unsupported"
	ReasonRedacted      InvalidReason = "redacted"
	ReasonTicketDeleted InvalidReason = "ticket_deleted"
)

// Event is one decision event, as the fold reads it. Wall-clock time and
// Lamport clock are carried for display and for tests that forge them; they
// never affect state.
type Event struct {
	EventID string
	// AuthorID is the comment's author_id, the actor.
	AuthorID string
	// To is the comment's addressee.
	To          string
	WallClockTS int64
	Lamport     int64

	// Class, Kind, Target and RequestKey come from the ingest of the
	// decision object. Decision is set only when Class is valid.
	Class         model.DecisionClass
	Kind          model.DecisionKind
	Target        string
	RequestKey    string
	PayloadSHA256 string
	Decision      *model.Decision

	// Redacted is set by the caller when the event body was redacted.
	Redacted bool
}

// Delete is one delete event. Cascade and Path are the optional additive
// payload fields; a delete without Cascade deletes its node only, and a
// delete without Path is matched by NodeID.
type Delete struct {
	UID     string
	NodeID  string
	Cascade bool
	Path    string
}

// TimelineEntry is one line of a card's timeline. Counted is false for an
// event that does not take part in the state.
type TimelineEntry struct {
	EventID     string
	Kind        model.DecisionKind
	AuthorID    string
	WallClockTS int64
	Counted     bool
}

// Card is the fold result for one request.
type Card struct {
	// Present is false when the request event is not in the input.
	Present   bool
	RequestID string
	State     State
	Flags     []Flag
	// InvalidReason is set when State is invalid.
	InvalidReason InvalidReason

	// PinnedAnswerID is the minimum event id among the answers named by
	// applied outcomes; set when State is applied.
	PinnedAnswerID string
	// EffectiveAnswerID is the answer to display: the pinned answer, or the
	// member of the agreeing set with the minimum event id.
	EffectiveAnswerID  string
	AgreeingAnswers    []string
	ConflictingAnswers []string
	RefusedAnswers     []string
	OutcomeIDs         []string
	WithdrawIDs        []string
	SupersededBy       []string

	Timeline []TimelineEntry
}

// HasFlag reports whether the card carries f.
func (c Card) HasFlag(f Flag) bool {
	for _, x := range c.Flags {
		if x == f {
			return true
		}
	}
	return false
}
