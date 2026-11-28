// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// ErrDecisionUnsupported distinguishes a future version or authority enum from
// malformed v1 data under ADR-008 section 3.2. Callers retain the underlying
// comment in either case.
var ErrDecisionUnsupported = fmt.Errorf("unsupported decision: %w", ErrInvalidInput)

// DecodedDecision retains the canonical digests and the raw duplicate-member signal.
// Inspect Payload.Kind and Target when routing an invalid or unsupported event.
type DecodedDecision struct {
	Payload        DecisionPayload
	PayloadSHA256  string
	IdentitySHA256 string
	Duplicates     bool
}

// DecodeDecision validates the decision member of a comment (MTIX-219.4).
// Supply the journal author for the answerer exclusion. Set authoring on the
// author and push paths.
func DecodeDecision(raw []byte, author string, authoring bool) (DecodedDecision, error) {
	canonical, duplicates, err := canonicalDecision(raw, authoring)
	if err != nil {
		return DecodedDecision{}, fmt.Errorf("decision wire: %w", err)
	}
	var fields map[string]any
	if err = json.Unmarshal(canonical, &fields); err != nil {
		return DecodedDecision{}, fmt.Errorf("decode canonical decision: %w: %w", ErrInvalidInput, err)
	}
	d := DecodedDecision{PayloadSHA256: decisionDigest(canonical), Duplicates: duplicates}
	d.Payload, err = readDecision(fields, author)
	delete(fields, "request_key")
	delete(fields, "supersedes")
	var identity bytes.Buffer
	writeDecisionJSON(&identity, fields)
	d.IdentitySHA256 = decisionDigest(identity.Bytes())
	if err != nil {
		return d, fmt.Errorf("validate decision: %w", err)
	}
	return d, nil
}

func decisionDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readDecision(fields map[string]any, author string) (DecisionPayload, error) {
	var err error
	o := decisionObject{decisionValue{fields, "decision", &err}, fields}
	// Preserve routing information even for future or malformed payloads.
	kind, _ := fields["kind"].(string)
	target, _ := fields["target"].(string)
	p := DecisionPayload{DecisionHeader: DecisionHeader{Kind: kind}, Target: target}
	v, ok := fields["v"].(float64)
	if !ok || math.Trunc(v) != v || v < 1 {
		return p, decisionInvalid("v")
	}
	if v > 1 {
		return p, fmt.Errorf("decision.v: %w", ErrDecisionUnsupported)
	}
	p.DecisionHeader = readDecisionHeader(o)
	if p.Kind != "request" && err == nil {
		p.Target = decisionField(o, "target", true, checkedString(decisionEventID.MatchString, false))
	}
	if err == nil {
		p.readKind(o, author)
	}
	return p, err
}

func (p *DecisionPayload) readKind(o decisionObject, author string) {
	switch p.Kind {
	case "request":
		r := readDecisionRequest(o)
		p.DecisionRequest = &r
		r.defaults(o.fields)
		if *o.err == nil {
			*o.err = r.validate(author)
		}
	case "answer":
		a := readDecisionAnswer(o)
		p.DecisionAnswer = &a
		if *o.err == nil && strings.HasPrefix(a.Channel, "frontend:") && (a.Attestation == nil || a.Attestation.Frontend != strings.TrimPrefix(a.Channel, "frontend:")) {
			*o.err = decisionInvalid("attestation")
		}
	case "outcome":
		x := readDecisionOutcome(o)
		p.DecisionOutcome = &x
		if *o.err == nil {
			*o.err = x.validate()
		}
	case "withdraw":
		w := readDecisionWithdraw(o)
		p.DecisionWithdraw = &w
		if w.ReasonCode == "" {
			w.ReasonCode = "other"
		}
		if *o.err == nil && w.Elsewhere != nil && w.ReasonCode != "answered_elsewhere" {
			*o.err = decisionInvalid("elsewhere requires answered_elsewhere")
		}
	}
}

func (r *DecisionRequest) defaults(fields map[string]any) {
	if _, ok := fields["requires_outcome"]; !ok {
		r.RequiresOutcome = true
	}
	if r.AnswerableBy == "" {
		r.AnswerableBy = "user"
	}
	if _, ok := fields["asker"]; !ok {
		r.Asker.Kind = "agent"
	}
}

func (o *DecisionOutcome) validate() error {
	if o.Result == "refused" {
		if o.Refusal == nil || o.Receipt != nil {
			return decisionInvalid("refused outcome requires refusal only")
		}
		if o.SeenAnswers == nil {
			o.SeenAnswers = []string{o.Answer}
		}
		return nil
	}
	if o.Refusal != nil || o.SeenAnswers != nil {
		return decisionInvalid("applied outcome carries refusal fields")
	}
	if o.Receipt == nil {
		o.Receipt = &DecisionReceipt{}
	}
	return nil
}

func (r DecisionRequest) validate(author string) error {
	if len(r.Ticket.Paths) != 1+len(r.Ticket.Ancestors) || len(r.Ticket.PathMatch) != len(r.Ticket.Paths) {
		return decisionInvalid("ticket parallel arrays")
	}
	if slices.Contains(r.Answerers, author) {
		return decisionInvalid("answerers includes asker")
	}
	ids := make(map[string]bool, len(r.Options))
	for _, o := range r.Options {
		if ids[o.ID] {
			return decisionInvalid("duplicate option id")
		}
		ids[o.ID] = true
	}
	if r.Recommend != nil && !ids[r.Recommend.Option] {
		return decisionInvalid("recommend.option")
	}
	return nil
}

// DecisionNormalizer yields the comparison value of an answer's free text under
// ADR-008 section 3.2. It never changes the wire hash. The model stays free of
// Unicode tables: the service layer supplies the trim-plus-NFC implementation.
type DecisionNormalizer func(string) (string, error)

// ValidateDecisionAnswer checks request-dependent wire fields (MTIX-219.4).
// Authorisation and journal references are resolved separately by the service and fold.
// Text that cannot be normalized is ErrInvalidInput.
func ValidateDecisionAnswer(request, answer DecisionPayload, normalize DecisionNormalizer) error {
	if request.DecisionRequest == nil || answer.DecisionAnswer == nil {
		return decisionInvalid("request and answer required")
	}
	if err := validateOpenedLinks(request.Links, answer.OpenedLinks); err != nil {
		return err
	}
	input, note, err := normalizeAnswerText(answer.DecisionAnswer, normalize)
	if err != nil {
		return err
	}
	if len(request.Options) == 0 {
		if answer.Option != "" || input != "" || note == "" {
			return decisionInvalid("free-text answer")
		}
		return nil
	}
	for _, o := range request.Options {
		if o.ID != answer.Option {
			continue
		}
		return o.validateAnswer(answer.InputValue, input, note)
	}
	return decisionInvalid("option")
}

func validateOpenedLinks(links []DecisionLink, opened []int) error {
	for _, index := range opened {
		if index < 0 || index >= len(links) {
			return decisionInvalid("opened_links index")
		}
	}
	for i, l := range links {
		if l.OpenBeforeAnswer && !slices.Contains(opened, i) {
			return decisionInvalid("link not opened")
		}
	}
	return nil
}

func normalizeAnswerText(a *DecisionAnswer, normalize DecisionNormalizer) (input, note string, err error) {
	if input, err = normalize(a.InputValue); err != nil {
		return "", "", fmt.Errorf("normalize input_value: %w", errors.Join(ErrInvalidInput, err))
	}
	if note, err = normalize(a.Note); err != nil {
		return "", "", fmt.Errorf("normalize note: %w", errors.Join(ErrInvalidInput, err))
	}
	return input, note, nil
}

func (o DecisionOption) validateAnswer(rawInput, input, note string) error {
	if o.NoteRequired && note == "" {
		return decisionInvalid("note required")
	}
	if o.Input == nil {
		if input != "" {
			return decisionInvalid("unexpected input_value")
		}
		return nil
	}
	if o.Input.Required && input == "" {
		return decisionInvalid("input_value required")
	}
	if o.Input.Type == "text" && len(rawInput) > 16<<10 {
		return decisionInvalid("input_value exceeds 16 KiB")
	}
	if input != "" && !decisionInputValid(o.Input.Type, input) {
		return decisionInvalid("input_value")
	}
	return nil
}

func decisionInputValid(kind, input string) bool {
	switch kind {
	case "text":
		return true
	case "finding_id", "story_ref":
		return decisionRef.MatchString(input)
	case "url":
		return validDecisionURL(input)
	default:
		return false
	}
}

// DecisionFallback renders the legacy comment body with its complete response
// instructions under MTIX-219.4.
func DecisionFallback(shortID, ticket, asker, question string) (string, error) {
	prefix := "DECISION NEEDED (" + shortID + "): "
	suffix := "\nAnswer with `mtix decisions answer " + shortID + "` (needs mtix 0.7.0 or later), or reply with `mtix comment " + ticket + " --to " + asker + " \"…\"`."
	budget := 2048 - len(prefix) - len(suffix)
	if budget < 0 {
		return "", decisionInvalid("fallback identifiers")
	}
	if len(question) > budget {
		for budget > 0 && !utf8.RuneStart(question[budget]) {
			budget--
		}
		question = question[:budget]
	}
	return prefix + question + suffix, nil
}
