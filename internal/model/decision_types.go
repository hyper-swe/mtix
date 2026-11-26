// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

// DecisionPayload is the validated v1 view. Exactly one kind-specific object is populated.
// The hashes in DecodedDecision cover the received object, including extension fields.
// See ADR-008 section 3.2.
type DecisionPayload struct {
	DecisionHeader
	Target string `json:"target,omitempty"`
	*DecisionRequest
	*DecisionAnswer
	*DecisionOutcome
	*DecisionWithdraw
}

// DecisionHeader carries the version, kind and author-scoped idempotency key.
// See ADR-008 section 3.2.
type DecisionHeader struct {
	V          int    `json:"v"`
	Kind       string `json:"kind"`
	RequestKey string `json:"request_key"`
}

// DecisionRequest describes a question and the conditions for answering it.
// See ADR-008 section 3.2.
type DecisionRequest struct {
	Question        string                  `json:"question"`
	Context         string                  `json:"context,omitempty"`
	Options         []DecisionOption        `json:"options,omitempty"`
	Recommend       *DecisionRecommendation `json:"recommend,omitempty"`
	Links           []DecisionLink          `json:"links,omitempty"`
	Deadline        string                  `json:"deadline,omitempty"`
	AnswerableBy    string                  `json:"answerable_by,omitempty"`
	Answerers       []string                `json:"answerers,omitempty"`
	RequiresOutcome bool                    `json:"requires_outcome"`
	Key             string                  `json:"key,omitempty"`
	Supersedes      []string                `json:"supersedes,omitempty"`
	Category        string                  `json:"category,omitempty"`
	Source          map[string]string       `json:"source,omitempty"`
	Ticket          DecisionTicket          `json:"ticket"`
	Asker           DecisionActor           `json:"asker,omitempty"`
}

// DecisionOption identifies one possible response and its input requirements.
// See ADR-008 section 3.2.
type DecisionOption struct {
	ID           string         `json:"id"`
	Label        string         `json:"label"`
	Consequence  string         `json:"consequence,omitempty"`
	Input        *DecisionInput `json:"input,omitempty"`
	NoteRequired bool           `json:"note_required,omitempty"`
}

// DecisionInput declares the grammar of an option’s input value.
// See ADR-008 section 3.2.
type DecisionInput struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// DecisionRecommendation records the asker’s preferred option and reason.
// See ADR-008 section 3.2.
type DecisionRecommendation struct {
	Option string `json:"option"`
	Reason string `json:"reason,omitempty"`
}

// DecisionLink describes a resource referenced by the request.
// See ADR-008 section 3.2.
type DecisionLink struct {
	Kind             string `json:"kind"`
	URL              string `json:"url"`
	Label            string `json:"label,omitempty"`
	Required         bool   `json:"required,omitempty"`
	OpenBeforeAnswer bool   `json:"open_before_answer,omitempty"`
}

// DecisionTicket records the immutable ticket chain at raise time, nearest ancestor first.
// See ADR-008 section 3.2.
type DecisionTicket struct {
	UID       string   `json:"uid"`
	Ancestors []string `json:"ancestors"`
	Paths     []string `json:"paths"`
	PathMatch []bool   `json:"path_match"`
	Truncated bool     `json:"truncated"`
}

// DecisionActor contains a declared kind and model.
// See ADR-008 section 3.2.
type DecisionActor struct {
	Kind  string `json:"kind"`
	Model string `json:"model,omitempty"`
}

// DecisionAnswer records a response and the evidence declared by its author.
// See ADR-008 section 3.2.
type DecisionAnswer struct {
	Option      string               `json:"option,omitempty"`
	InputValue  string               `json:"input_value,omitempty"`
	Note        string               `json:"note,omitempty"`
	Decider     DecisionActor        `json:"decider"`
	Channel     string               `json:"channel"`
	Evidence    DecisionEvidence     `json:"evidence,omitempty"`
	Attestation *DecisionAttestation `json:"attestation,omitempty"`
	OpenedLinks []int                `json:"opened_links,omitempty"`
}

// DecisionEvidence records declared process and terminal information. A nil TTYSession denotes no controlling terminal.
// See ADR-008 section 3.2.
type DecisionEvidence struct {
	Ancestors    []string `json:"ancestors,omitempty"`
	TTYSession   *int64   `json:"tty_session,omitempty"`
	MarkersUnset []string `json:"markers_unset,omitempty"`
}

// DecisionAttestation identifies the front end, user and time of a declared click.
// See ADR-008 section 3.2.
type DecisionAttestation struct {
	Frontend  string `json:"frontend"`
	User      string `json:"user"`
	ClickedAt string `json:"clicked_at"`
}

// DecisionOutcome records whether the asker applied or refused an answer.
// See ADR-008 section 3.2.
type DecisionOutcome struct {
	Answer      string           `json:"answer"`
	SeenAnswers []string         `json:"seen_answers,omitzero"`
	Result      string           `json:"result"`
	Receipt     *DecisionReceipt `json:"receipt,omitempty"`
	Refusal     *DecisionRefusal `json:"refusal,omitempty"`
}

// DecisionReceipt carries the asker’s declared evidence of an applied outcome.
// See ADR-008 section 3.2.
type DecisionReceipt struct {
	Ledger string  `json:"ledger,omitempty"`
	Run    string  `json:"run,omitempty"`
	Seq    float64 `json:"seq,omitempty"`
	Hash   string  `json:"hash,omitempty"`
	Kind   string  `json:"kind,omitempty"`
	At     string  `json:"at,omitempty"`
}

// DecisionRefusal retains the verbatim explanation for a refused outcome.
// See ADR-008 section 3.2.
type DecisionRefusal struct {
	Text string `json:"text"`
	Code string `json:"code,omitempty"`
}

// DecisionWithdraw records why the asker withdrew a request.
// See ADR-008 section 3.2.
type DecisionWithdraw struct {
	ReasonCode string             `json:"reason_code,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Elsewhere  *DecisionElsewhere `json:"elsewhere,omitempty"`
}

// DecisionElsewhere describes an answer the asker received outside the decision channel.
// See ADR-008 section 3.2.
type DecisionElsewhere struct {
	Channel string          `json:"channel"`
	Decider DecisionActor   `json:"decider"`
	Receipt DecisionReceipt `json:"receipt,omitempty"`
}
