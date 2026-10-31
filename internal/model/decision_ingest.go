// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"slices"
)

// DecisionIngest is the result of reading the raw bytes of a decision
// object. Reading never fails the comment that carries the object: the
// result is a classification plus, for a valid object, the decoded value.
type DecisionIngest struct {
	// Class is valid, malformed or unsupported.
	Class DecisionClass
	// Reason is the first problem found; empty for a valid object.
	Reason string
	// Kind is the kind as received; it can be an unknown value.
	Kind DecisionKind
	// Canonical is the RFC 8785 form of the object as received, unknown
	// fields included. Empty when the bytes do not parse.
	Canonical []byte
	// PayloadSHA256 is the hex SHA-256 of Canonical.
	PayloadSHA256 string
	// ContentSHA256 is the same hash after removing request_key and
	// supersedes, so two requests with the same content share it.
	ContentSHA256 string
	// HadDuplicates reports that a member name was repeated; the later
	// member won.
	HadDuplicates bool
	// Value is the decoded object. It is set only when Class is valid.
	Value *Decision
}

// IngestDecision reads the raw bytes of a decision object the same way on
// every transport: parse (a repeated member name replaces the earlier one),
// serialize in RFC 8785 form, hash, and decode the typed value from the
// canonical bytes only.
func IngestDecision(raw []byte) DecisionIngest {
	if len(raw) > MaxDecisionCommentPayloadBytes {
		return malformedIngest(fmt.Sprintf("decision: longer than %d bytes", MaxDecisionCommentPayloadBytes))
	}
	parsed, dup, err := parseJSON(raw, MaxDecisionCanonicalDepth)
	if err != nil {
		return malformedIngest("decision: " + err.Error())
	}
	canon, err := canonicalValue(parsed)
	if err != nil {
		return malformedIngest("decision: " + err.Error())
	}
	res := DecisionIngest{
		Canonical:     canon,
		PayloadSHA256: sha256Hex(canon),
		ContentSHA256: contentHash(parsed, canon),
		HadDuplicates: dup,
	}
	// Decode from the canonical bytes, never from the raw bytes.
	again, _, err := parseJSON(canon, MaxDecisionCanonicalDepth)
	if err != nil {
		res.Class, res.Reason = DecisionMalformed, "decision: "+err.Error()
		return res
	}
	c := &decisionCheck{}
	res.Kind, res.Value = decodeDecision(c, again)
	res.Class, res.Reason = c.class()
	if res.Class != DecisionValid {
		res.Value = nil
	}
	return res
}

func malformedIngest(reason string) DecisionIngest {
	return DecisionIngest{Class: DecisionMalformed, Reason: reason}
}

// contentHash hashes the object without request_key and supersedes.
func contentHash(parsed any, canon []byte) string {
	m, ok := parsed.(map[string]any)
	if !ok {
		return sha256Hex(canon)
	}
	stripped := make(map[string]any, len(m))
	for k, v := range m {
		if k != "request_key" && k != "supersedes" {
			stripped[k] = v
		}
	}
	b, err := canonicalValue(stripped)
	if err != nil {
		return sha256Hex(canon)
	}
	return sha256Hex(b)
}

// decodeDecision checks a parsed decision object and decodes it.
func decodeDecision(c *decisionCheck, v any) (DecisionKind, *Decision) {
	o, ok := c.root(v)
	if !ok {
		return "", nil
	}
	kind := DecisionKind("")
	if s, isStr := o.m["kind"].(string); isStr {
		kind = DecisionKind(s)
	}
	ver, _ := o.integer("v", true, 1)
	if ver > DecisionWireVersion {
		c.unsup("decision.v", "version %d needs a newer reader", ver)
		return kind, nil
	}
	k := o.enum("kind", "", string(DecisionKindRequest), string(DecisionKindAnswer),
		string(DecisionKindOutcome), string(DecisionKindWithdraw))
	d := &Decision{
		V:          int(ver),
		Kind:       DecisionKind(k),
		RequestKey: o.str("request_key", strRule{required: true, minChars: 1, maxChars: maxRequestKeyChars, pattern: decisionTokenPattern}),
	}
	switch d.Kind {
	case DecisionKindRequest:
		d.Request = decodeRequest(o)
	case DecisionKindAnswer:
		d.Answer = decodeAnswer(o)
	case DecisionKindOutcome:
		d.Outcome = decodeOutcome(o)
	case DecisionKindWithdraw:
		d.Withdraw = decodeWithdraw(o)
	}
	return kind, d
}

// IngestDecisionFromComment reads the decision object of a comment payload.
// ok is false when the payload does not parse, exceeds the payload limits
// or carries no decision member; such a comment stays an ordinary comment.
func IngestDecisionFromComment(payload []byte) (res DecisionIngest, ok bool) {
	if len(payload) > MaxDecisionCommentPayloadBytes {
		return DecisionIngest{}, false
	}
	_, span, found, err := parseJSONSpan(payload, MaxDecisionNestingDepth, "decision")
	if err != nil || !found || string(span) == "null" {
		return DecisionIngest{}, false
	}
	return IngestDecision(span), true
}

// ValidateDecisionForWrite checks the raw bytes of a decision object this
// binary is about to write. Unlike ingest it refuses repeated member names,
// numbers that are not finite IEEE doubles and any object that is not a
// valid v1 decision. It returns a wrapped ErrInvalidInput.
func ValidateDecisionForWrite(raw []byte) error {
	if len(raw) > MaxDecisionCommentPayloadBytes {
		return fmt.Errorf("decision longer than %d bytes: %w", MaxDecisionCommentPayloadBytes, ErrInvalidInput)
	}
	_, dup, err := parseJSON(raw, MaxDecisionCanonicalDepth)
	if err != nil {
		return err
	}
	if dup {
		return fmt.Errorf("decision repeats a member name: %w", ErrInvalidInput)
	}
	if res := IngestDecision(raw); res.Class != DecisionValid {
		return fmt.Errorf("decision is %s (%s): %w", res.Class, res.Reason, ErrInvalidInput)
	}
	return nil
}

// ValidateDecisionCommentForWrite checks a whole comment payload this
// binary is about to write: it must fit the payload size and nesting limits,
// repeat no member name, and carry a valid v1 decision when it has one.
func ValidateDecisionCommentForWrite(payload []byte) error {
	if len(payload) > MaxDecisionCommentPayloadBytes {
		return fmt.Errorf("comment payload longer than %d bytes: %w", MaxDecisionCommentPayloadBytes, ErrInvalidInput)
	}
	_, span, found, err := parseJSONSpan(payload, MaxDecisionNestingDepth, "decision")
	if err != nil {
		return err
	}
	if dup, err := JSONHasDuplicateNames(payload); err != nil || dup {
		if err != nil {
			return err
		}
		return fmt.Errorf("comment payload repeats a member name: %w", ErrInvalidInput)
	}
	if !found {
		return nil
	}
	return ValidateDecisionForWrite(span)
}

// DecisionKinds lists the decision kinds this binary understands.
func DecisionKinds() []DecisionKind {
	return slices.Clone([]DecisionKind{DecisionKindRequest, DecisionKindAnswer, DecisionKindOutcome, DecisionKindWithdraw})
}
