// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "strings"

var decisionEventRef = strRule{required: true, pattern: decisionEventID}

// decodeAnswer reads the answer-specific fields of a decision object.
func decodeAnswer(o *fieldObj) *DecisionAnswer {
	a := &DecisionAnswer{
		Target:     o.str("target", decisionEventRef),
		Option:     o.str("option", strRule{pattern: decisionOptionID}),
		InputValue: o.str("input_value", strRule{maxBytes: maxInputTextBytes}),
		Note:       o.str("note", strRule{maxBytes: maxNoteBytes}),
		Decider:    o.actor("decider", true, true, nil),
		Channel:    o.str("channel", strRule{required: true, pattern: decisionChannel}),
	}
	a.Evidence = decodeEvidence(o)
	a.Attestation = decodeAttestation(o)
	if strings.HasPrefix(a.Channel, "frontend:") {
		if len(a.Channel) == len("frontend:") {
			o.c.fail(o.at("channel"), "frontend channel needs a name")
		}
		if a.Attestation == nil {
			o.c.fail(o.at("attestation"), "required for a frontend channel")
		}
	}
	a.OpenedLinks = []int{}
	seen := map[int64]bool{}
	for _, e := range o.array("opened_links", false, maxOpenedLinks) {
		n, ok := o.asInt("opened_links", e, 0)
		if !ok || n >= maxLinks {
			o.c.fail(o.at("opened_links"), "entries must be link indexes from 0 to %d", maxLinks-1)
			continue
		}
		if seen[n] {
			o.c.fail(o.at("opened_links"), "duplicate link index")
		}
		seen[n] = true
		a.OpenedLinks = append(a.OpenedLinks, int(n))
	}
	return a
}

func decodeEvidence(o *fieldObj) DecisionEvidence {
	var ev DecisionEvidence
	sub := o.object("evidence", false)
	if sub == nil {
		return ev
	}
	name := strRule{minChars: 1, maxChars: maxNameChars, oneLine: true}
	ev.Ancestors = sub.strings("ancestors", maxEvidenceNames, name)
	ev.TTYSession = sub.str("tty_session", strRule{maxChars: maxTTYSessionChars, oneLine: true})
	ev.MarkersUnset = sub.strings("markers_unset", maxMarkers, name)
	return ev
}

func decodeAttestation(o *fieldObj) *DecisionAttestation {
	sub := o.object("attestation", false)
	if sub == nil {
		return nil
	}
	return &DecisionAttestation{
		Frontend:  sub.str("frontend", strRule{required: true, minChars: 1, maxChars: maxNameChars, oneLine: true}),
		User:      sub.str("user", strRule{required: true, minChars: 1, maxChars: maxAttestUserChars, oneLine: true}),
		ClickedAt: sub.timestampAny("clicked_at", true),
	}
}

// decodeOutcome reads the outcome-specific fields of a decision object.
func decodeOutcome(o *fieldObj) *DecisionOutcome {
	out := &DecisionOutcome{
		Target: o.str("target", decisionEventRef),
		Answer: o.str("answer", decisionEventRef),
		Result: o.enum("result", "", DecisionResultApplied, DecisionResultRefused),
	}
	out.SeenAnswers = o.strings("seen_answers", maxSeenAnswers, strRule{minChars: 1, pattern: decisionEventID})
	if hasDuplicates(out.SeenAnswers) {
		o.c.fail(o.at("seen_answers"), "duplicate answer id")
	}
	if len(out.SeenAnswers) == 0 {
		out.SeenAnswers = []string{out.Answer}
	}
	if sub := o.object("receipt", false); sub != nil {
		out.Receipt = decodeReceipt(sub)
	}
	if sub := o.object("refusal", out.Result == DecisionResultRefused); sub != nil {
		out.Refusal = &DecisionRefusal{
			Text: sub.str("text", strRule{required: true, minChars: 1, maxBytes: maxRefusalBytes}),
			Code: sub.str("code", strRule{maxChars: maxRefusalCode, oneLine: true}),
		}
	}
	return out
}

func decodeReceipt(o *fieldObj) DecisionReceipt {
	r := DecisionReceipt{
		Ledger: o.str("ledger", strRule{maxChars: 64, oneLine: true}),
		Run:    o.str("run", strRule{maxChars: 128, oneLine: true}),
		Hash:   o.str("hash", strRule{maxChars: 128, pattern: decisionHex}),
		Kind:   o.str("kind", strRule{maxChars: 32, oneLine: true}),
		At:     o.timestampAny("at", false),
	}
	if _, present := o.m["hash"]; present && r.Hash == "" {
		o.c.fail(o.at("hash"), "must be a hexadecimal string")
	}
	seq, _ := o.integer("seq", false, 0)
	r.Seq = seq
	return r
}

// decodeWithdraw reads the withdraw-specific fields of a decision object.
func decodeWithdraw(o *fieldObj) *DecisionWithdraw {
	w := &DecisionWithdraw{
		Target:     o.str("target", decisionEventRef),
		ReasonCode: o.str("reason_code", strRule{pattern: decisionDisplayEnum}),
		Reason:     o.str("reason", strRule{maxBytes: maxWithdrawReason}),
	}
	if w.ReasonCode == "" {
		w.ReasonCode = "other"
	}
	if sub := o.object("elsewhere", false); sub != nil {
		w.Elsewhere = &DecisionElsewhere{
			Channel: sub.str("channel", strRule{maxChars: maxChannelChars, oneLine: true}),
			Decider: sub.actor("decider", false, true, &DecisionActor{}),
		}
		if r := sub.object("receipt", false); r != nil {
			w.Elsewhere.Receipt = decodeReceipt(r)
		}
	}
	return w
}
