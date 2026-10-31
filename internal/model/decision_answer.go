// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// DecisionShortID returns the short id of a request: "d-" and the first
// eight hex characters of the SHA-256 of the request event id.
func DecisionShortID(requestEventID string) string {
	sum := sha256.Sum256([]byte(requestEventID))
	return "d-" + hex.EncodeToString(sum[:])[:8]
}

// NormalizeDecisionText normalises a value for comparing answers: it trims
// leading and trailing whitespace. Case and inner whitespace count. A value
// that is missing equals the empty string.
//
// Known gap: Unicode NFC normalisation is not applied, because the
// standard library has no NFC and golang.org/x/text is not an approved
// package. Two values that differ only in Unicode normal form compare as
// different until that package is approved. This is the single place that
// would apply it.
func NormalizeDecisionText(s string) string {
	return strings.TrimSpace(s)
}

// DecisionAnswerValue is the triple that decides whether two answers agree.
type DecisionAnswerValue struct {
	Option     string
	InputValue string
	Note       string
}

// Value returns the normalised value of the answer. Two answers agree only
// when all three parts are equal.
func (a *DecisionAnswer) Value() DecisionAnswerValue {
	return DecisionAnswerValue{
		Option:     a.Option,
		InputValue: NormalizeDecisionText(a.InputValue),
		Note:       NormalizeDecisionText(a.Note),
	}
}

// ValidateDecisionAnswer checks an answer against the request it targets:
// the option exists, input_value fits the option's input rule, a note is
// present where one is required, opened_links index the request's links and
// the decider kind is allowed by answerable_by. It returns a wrapped
// ErrInvalidInput. It does not look at who wrote the answer.
func ValidateDecisionAnswer(req *DecisionRequest, ans *DecisionAnswer) error {
	if req == nil || ans == nil {
		return fmt.Errorf("decision answer check needs a request and an answer: %w", ErrInvalidInput)
	}
	if req.AnswerableBy == DecisionAnswerableHuman && ans.Decider.Kind != DecisionActorHuman {
		return fmt.Errorf("this request accepts answers from a human only: %w", ErrInvalidInput)
	}
	for _, i := range ans.OpenedLinks {
		if i < 0 || i >= len(req.Links) {
			return fmt.Errorf("opened link %d is not a link of the request: %w", i, ErrInvalidInput)
		}
	}
	note := NormalizeDecisionText(ans.Note)
	if len(req.Options) == 0 {
		return checkFreeText(ans, note)
	}
	idx := slices.IndexFunc(req.Options, func(o DecisionOption) bool { return o.ID == ans.Option })
	if idx < 0 {
		return fmt.Errorf("option %q is not an option of the request: %w", clip(ans.Option), ErrInvalidInput)
	}
	opt := req.Options[idx]
	if opt.NoteRequired && note == "" {
		return fmt.Errorf("option %q needs a note: %w", opt.ID, ErrInvalidInput)
	}
	return checkInputValue(opt, ans.InputValue)
}

func checkFreeText(ans *DecisionAnswer, note string) error {
	if ans.Option != "" || NormalizeDecisionText(ans.InputValue) != "" {
		return fmt.Errorf("a request without options takes a note only: %w", ErrInvalidInput)
	}
	if note == "" {
		return fmt.Errorf("a request without options needs a non-empty note: %w", ErrInvalidInput)
	}
	return nil
}

func checkInputValue(opt DecisionOption, raw string) error {
	v := NormalizeDecisionText(raw)
	if opt.Input == nil {
		if v != "" {
			return fmt.Errorf("option %q takes no input: %w", opt.ID, ErrInvalidInput)
		}
		return nil
	}
	if v == "" {
		if opt.Input.Required {
			return fmt.Errorf("option %q needs an input value: %w", opt.ID, ErrInvalidInput)
		}
		return nil
	}
	switch opt.Input.Type {
	case DecisionInputText:
		if len(v) > maxInputTextBytes || !utf8.ValidString(v) {
			return fmt.Errorf("input value is too long: %w", ErrInvalidInput)
		}
	case DecisionInputFindingID, DecisionInputStoryRef:
		if !decisionOpaqueRef.MatchString(v) {
			return fmt.Errorf("input value does not match %s: %w", decisionOpaqueRef, ErrInvalidInput)
		}
	case DecisionInputURL:
		return ValidateDecisionLinkURL(v)
	default:
		return fmt.Errorf("option %q has an unknown input type: %w", opt.ID, ErrInvalidInput)
	}
	return nil
}

// ValidateDecisionRequestAuthor checks the parts of a request that depend
// on who wrote it: the answerers never include the asker.
func ValidateDecisionRequestAuthor(req *DecisionRequest, askerID string) error {
	if req != nil && slices.Contains(req.Answerers, askerID) {
		return fmt.Errorf("answerers must not include the asker: %w", ErrInvalidInput)
	}
	return nil
}
