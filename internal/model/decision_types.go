// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

// Decision wire-format limits. MaxDecisionCommentPayloadBytes and
// MaxDecisionNestingDepth mirror the sync payload limits enforced by the
// sync validator; a test keeps the two in step.
const (
	// DecisionWireVersion is the only decision version this binary reads.
	DecisionWireVersion = 1

	// MaxDecisionCommentPayloadBytes is the cap on a whole comment payload.
	MaxDecisionCommentPayloadBytes = 64 * 1024
	// MaxDecisionNestingDepth is the cap on JSON nesting in a comment payload.
	MaxDecisionNestingDepth = 10
	// MaxDecisionCanonicalDepth is the nesting cap for the decision object
	// itself, which sits one level inside the comment payload.
	MaxDecisionCanonicalDepth = MaxDecisionNestingDepth - 1

	maxQuestionChars    = 300
	maxContextBytes     = 32 * 1024
	maxOptions          = 9
	maxLabelChars       = 80
	maxConsequenceChars = 160
	maxInputTextBytes   = 16 * 1024
	maxNoteBytes        = 16 * 1024
	maxReasonChars      = 300
	maxLinks            = 10
	maxLinkURLBytes     = 2048
	maxAnswerers        = 8
	maxSupersedes       = 4
	maxSourcePairs      = 8
	maxSourceValueChars = 120
	maxAncestors        = 64
	maxUIDBytes         = 64
	maxPathBytes        = 256
	maxModelChars       = 80
	maxRequestKeyChars  = 64
	maxKeyChars         = 128
	maxSeenAnswers      = 32
	maxEvidenceNames    = 8
	maxMarkers          = 32
	maxNameChars        = 64
	maxTTYSessionChars  = 128
	maxAttestUserChars  = 120
	maxRefusalBytes     = 16 * 1024
	maxRefusalCode      = 64
	maxWithdrawReason   = 2 * 1024
	maxOpenedLinks      = maxLinks
	maxChannelChars     = 64
)

// DecisionKind is the kind of a decision event.
type DecisionKind string

// Decision event kinds.
const (
	DecisionKindRequest  DecisionKind = "request"
	DecisionKindAnswer   DecisionKind = "answer"
	DecisionKindOutcome  DecisionKind = "outcome"
	DecisionKindWithdraw DecisionKind = "withdraw"
)

// Authority enum values.
const (
	DecisionAnswerableHuman = "human"
	DecisionAnswerableAny   = "any"

	DecisionActorHuman = "human"
	DecisionActorAgent = "agent"

	DecisionResultApplied = "applied"
	DecisionResultRefused = "refused"

	DecisionInputText      = "text"
	DecisionInputFindingID = "finding_id"
	DecisionInputStoryRef  = "story_ref"
	DecisionInputURL       = "url"
)

// DecisionClass is the classification of a decision object on ingest.
type DecisionClass string

// Decision classifications. A classification never rejects the comment that
// carries the decision.
const (
	// DecisionValid means the object is a well-formed v1 decision.
	DecisionValid DecisionClass = "valid"
	// DecisionMalformed means a v1 object breaks a field rule.
	DecisionMalformed DecisionClass = "malformed"
	// DecisionUnsupported means the object needs a newer reader: a version
	// above 1 or an unknown authority enum value.
	DecisionUnsupported DecisionClass = "unsupported"
)

// DecisionActor is a declared actor.
type DecisionActor struct {
	Kind  string `json:"kind"`
	Model string `json:"model,omitempty"`
}

// DecisionInput describes the extra input an option asks for.
type DecisionInput struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// DecisionOption is one answer choice of a request.
type DecisionOption struct {
	ID           string         `json:"id"`
	Label        string         `json:"label,omitempty"`
	Consequence  string         `json:"consequence,omitempty"`
	NoteRequired bool           `json:"note_required,omitempty"`
	Input        *DecisionInput `json:"input,omitempty"`
}

// DecisionRecommend names the option the asker recommends.
type DecisionRecommend struct {
	Option string `json:"option"`
	Reason string `json:"reason,omitempty"`
}

// DecisionLink is a link attached to a request.
type DecisionLink struct {
	Kind             string `json:"kind"`
	URL              string `json:"url"`
	Label            string `json:"label,omitempty"`
	Required         bool   `json:"required,omitempty"`
	OpenBeforeAnswer bool   `json:"open_before_answer,omitempty"`
}

// DecisionTicket locates the ticket a request is about.
type DecisionTicket struct {
	UID       string   `json:"uid"`
	Ancestors []string `json:"ancestors"`
	Paths     []string `json:"paths"`
	PathMatch []bool   `json:"path_match"`
	Truncated bool     `json:"truncated"`
}

// DecisionReceipt is the asker's receipt of an applied answer.
type DecisionReceipt struct {
	Ledger string `json:"ledger,omitempty"`
	Run    string `json:"run,omitempty"`
	Seq    int64  `json:"seq,omitempty"`
	Hash   string `json:"hash,omitempty"`
	Kind   string `json:"kind,omitempty"`
	At     string `json:"at,omitempty"`
}

// DecisionRefusal records why the asker refused an answer.
type DecisionRefusal struct {
	Text string `json:"text"`
	Code string `json:"code,omitempty"`
}

// DecisionElsewhere records where a withdrawn request was answered.
type DecisionElsewhere struct {
	Channel string          `json:"channel,omitempty"`
	Decider DecisionActor   `json:"decider"`
	Receipt DecisionReceipt `json:"receipt"`
}

// DecisionEvidence is the declared environment of an answer.
type DecisionEvidence struct {
	Ancestors    []string `json:"ancestors,omitempty"`
	TTYSession   string   `json:"tty_session,omitempty"`
	MarkersUnset []string `json:"markers_unset,omitempty"`
}

// DecisionAttestation is a front end's statement about a click.
type DecisionAttestation struct {
	Frontend  string `json:"frontend"`
	User      string `json:"user"`
	ClickedAt string `json:"clicked_at"`
}

// DecisionRequest holds the request-specific fields.
type DecisionRequest struct {
	Question        string             `json:"question"`
	Context         string             `json:"context,omitempty"`
	Options         []DecisionOption   `json:"options"`
	Recommend       *DecisionRecommend `json:"recommend,omitempty"`
	Links           []DecisionLink     `json:"links"`
	Deadline        string             `json:"deadline,omitempty"`
	AnswerableBy    string             `json:"answerable_by"`
	Answerers       []string           `json:"answerers"`
	RequiresOutcome bool               `json:"requires_outcome"`
	Key             string             `json:"key,omitempty"`
	Supersedes      []string           `json:"supersedes"`
	Category        string             `json:"category,omitempty"`
	Source          map[string]string  `json:"source"`
	Asker           DecisionActor      `json:"asker"`
	Ticket          DecisionTicket     `json:"ticket"`
}

// DecisionAnswer holds the answer-specific fields.
type DecisionAnswer struct {
	Target      string               `json:"target"`
	Option      string               `json:"option,omitempty"`
	InputValue  string               `json:"input_value,omitempty"`
	Note        string               `json:"note,omitempty"`
	Decider     DecisionActor        `json:"decider"`
	Channel     string               `json:"channel"`
	Evidence    DecisionEvidence     `json:"evidence"`
	Attestation *DecisionAttestation `json:"attestation,omitempty"`
	OpenedLinks []int                `json:"opened_links"`
}

// DecisionOutcome holds the outcome-specific fields.
type DecisionOutcome struct {
	Target      string           `json:"target"`
	Answer      string           `json:"answer"`
	SeenAnswers []string         `json:"seen_answers"`
	Result      string           `json:"result"`
	Receipt     DecisionReceipt  `json:"receipt"`
	Refusal     *DecisionRefusal `json:"refusal,omitempty"`
}

// DecisionWithdraw holds the withdraw-specific fields.
type DecisionWithdraw struct {
	Target     string             `json:"target"`
	ReasonCode string             `json:"reason_code"`
	Reason     string             `json:"reason,omitempty"`
	Elsewhere  *DecisionElsewhere `json:"elsewhere,omitempty"`
}

// Decision is a decoded decision object. Exactly one of Request, Answer,
// Outcome and Withdraw is set, matching Kind.
type Decision struct {
	V          int          `json:"v"`
	Kind       DecisionKind `json:"kind"`
	RequestKey string       `json:"request_key"`

	Request  *DecisionRequest  `json:"-"`
	Answer   *DecisionAnswer   `json:"-"`
	Outcome  *DecisionOutcome  `json:"-"`
	Withdraw *DecisionWithdraw `json:"-"`
}
