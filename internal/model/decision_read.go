// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

func readDecisionHeader(o decisionObject) DecisionHeader {
	return DecisionHeader{
		V:          1, // readDecision has checked the canonical version.
		Kind:       decisionField(o, "kind", true, checkedString(oneOf("request", "answer", "outcome", "withdraw"), true)),
		RequestKey: decisionField(o, "request_key", true, checkedString(requestKey.MatchString, false)),
	}
}

func readDecisionRequest(o decisionObject) DecisionRequest {
	return DecisionRequest{
		Question:        decisionField(o, "question", true, checkedString(decisionQuestion, false)),
		Context:         decisionField(o, "context", false, checkedString(atMostBytes(32768), false)),
		Options:         decisionField(o, "options", false, decisionList(9, decisionNested(readDecisionOption))),
		Recommend:       decisionField(o, "recommend", false, decisionPointer(decisionNested(readDecisionRecommendation))),
		Links:           decisionField(o, "links", false, decisionList(10, decisionNested(readDecisionLink))),
		Deadline:        decisionField(o, "deadline", false, checkedString(validDecisionUTC, false)),
		AnswerableBy:    decisionField(o, "answerable_by", false, checkedString(oneOf("user", "any"), true)),
		Answerers:       decisionField(o, "answerers", false, decisionList(8, checkedString(IsValidAuthorID, false))),
		RequiresOutcome: decisionField(o, "requires_outcome", false, decisionBool),
		Key:             decisionField(o, "key", false, checkedString(decisionKey.MatchString, false)),
		Supersedes:      decisionField(o, "supersedes", false, decisionList(4, checkedString(decisionEventID.MatchString, false))),
		Category:        decisionField(o, "category", false, checkedString(decisionCategory.MatchString, false)),
		Source:          decisionField(o, "source", false, decisionSource),
		Ticket:          decisionField(o, "ticket", true, decisionNested(readDecisionTicket)),
		Asker:           decisionField(o, "asker", false, decisionNested(readDecisionActor)),
	}
}

func readDecisionOption(o decisionObject) DecisionOption {
	return DecisionOption{
		ID:           decisionField(o, "id", true, checkedString(optionID.MatchString, false)),
		Label:        decisionField(o, "label", true, checkedString(atMostChars(80), false)),
		Consequence:  decisionField(o, "consequence", false, checkedString(decisionConsequence, false)),
		Input:        decisionField(o, "input", false, decisionPointer(decisionNested(readDecisionInput))),
		NoteRequired: decisionField(o, "note_required", false, decisionBool),
	}
}

func readDecisionInput(o decisionObject) DecisionInput {
	return DecisionInput{
		Type:     decisionField(o, "type", true, checkedString(oneOf("text", "finding_id", "story_ref", "url"), true)),
		Required: decisionField(o, "required", true, decisionBool),
	}
}

func readDecisionRecommendation(o decisionObject) DecisionRecommendation {
	return DecisionRecommendation{
		Option: decisionField(o, "option", true, checkedString(optionID.MatchString, false)),
		Reason: decisionField(o, "reason", false, checkedString(atMostChars(300), false)),
	}
}

func readDecisionLink(o decisionObject) DecisionLink {
	return DecisionLink{
		Kind:             decisionField(o, "kind", true, checkedString(nonempty, false)),
		URL:              decisionField(o, "url", true, checkedString(validDecisionURL, false)),
		Label:            decisionField(o, "label", false, checkedString(atMostChars(80), false)),
		Required:         decisionField(o, "required", false, decisionBool),
		OpenBeforeAnswer: decisionField(o, "open_before_answer", false, decisionBool),
	}
}

func readDecisionTicket(o decisionObject) DecisionTicket {
	return DecisionTicket{
		UID:       decisionField(o, "uid", true, checkedString(nonempty, false)),
		Ancestors: decisionField(o, "ancestors", true, decisionList(64, checkedString(nil, false))),
		Paths:     decisionField(o, "paths", true, decisionList(65, checkedString(nonempty, false))),
		PathMatch: decisionField(o, "path_match", true, decisionList(65, decisionBool)),
		Truncated: decisionField(o, "truncated", true, decisionBool),
	}
}

func readDecisionActor(o decisionObject) DecisionActor {
	return DecisionActor{
		Kind:  decisionField(o, "kind", true, checkedString(oneOf("agent", "user"), o.path != "decision.asker")),
		Model: decisionField(o, "model", false, checkedString(atMostChars(80), false)),
	}
}

func readDecisionAnswer(o decisionObject) DecisionAnswer {
	return DecisionAnswer{
		Option:      decisionField(o, "option", false, checkedString(optionID.MatchString, false)),
		InputValue:  decisionField(o, "input_value", false, checkedString(nil, false)),
		Note:        decisionField(o, "note", false, checkedString(atMostBytes(16384), false)),
		Decider:     decisionField(o, "decider", true, decisionNested(readDecisionActor)),
		Channel:     decisionField(o, "channel", true, checkedString(nonempty, false)),
		Evidence:    decisionField(o, "evidence", false, decisionNested(readDecisionEvidence)),
		Attestation: decisionField(o, "attestation", false, decisionPointer(decisionNested(readDecisionAttestation))),
		OpenedLinks: decisionField(o, "opened_links", false, decisionList(0, decisionIndex)),
	}
}

func readDecisionEvidence(o decisionObject) DecisionEvidence {
	return DecisionEvidence{
		Ancestors:    decisionField(o, "ancestors", false, decisionList(8, checkedString(nil, false))),
		TTYSession:   decisionField(o, "tty_session", false, decisionPointer(decisionSession)),
		MarkersUnset: decisionField(o, "markers_unset", false, decisionList(0, checkedString(nil, false))),
	}
}

func readDecisionAttestation(o decisionObject) DecisionAttestation {
	return DecisionAttestation{
		Frontend:  decisionField(o, "frontend", true, checkedString(nonempty, false)),
		User:      decisionField(o, "user", true, checkedString(atMostChars(120), false)),
		ClickedAt: decisionField(o, "clicked_at", true, checkedString(validDecisionTime, false)),
	}
}

func readDecisionOutcome(o decisionObject) DecisionOutcome {
	return DecisionOutcome{
		Answer:      decisionField(o, "answer", true, checkedString(decisionEventID.MatchString, false)),
		SeenAnswers: decisionField(o, "seen_answers", false, decisionList(32, checkedString(decisionEventID.MatchString, false))),
		Result:      decisionField(o, "result", true, checkedString(oneOf("applied", "refused"), true)),
		Receipt:     decisionField(o, "receipt", false, decisionPointer(decisionNested(readDecisionReceipt))),
		Refusal:     decisionField(o, "refusal", false, decisionPointer(decisionNested(readDecisionRefusal))),
	}
}

func readDecisionReceipt(o decisionObject) DecisionReceipt {
	return DecisionReceipt{
		Ledger: decisionField(o, "ledger", false, checkedString(atMostChars(64), false)),
		Run:    decisionField(o, "run", false, checkedString(atMostChars(128), false)),
		Seq:    decisionField(o, "seq", false, decisionSequence),
		Hash:   decisionField(o, "hash", false, checkedString(receiptHash.MatchString, false)),
		Kind:   decisionField(o, "kind", false, checkedString(atMostChars(32), false)),
		At:     decisionField(o, "at", false, checkedString(validDecisionTime, false)),
	}
}

func readDecisionRefusal(o decisionObject) DecisionRefusal {
	return DecisionRefusal{
		Text: decisionField(o, "text", true, checkedString(atMostBytes(16384), false)),
		Code: decisionField(o, "code", false, checkedString(atMostChars(64), false)),
	}
}

func readDecisionWithdraw(o decisionObject) DecisionWithdraw {
	return DecisionWithdraw{
		ReasonCode: decisionField(o, "reason_code", false, checkedString(nonempty, false)),
		Reason:     decisionField(o, "reason", false, checkedString(atMostBytes(2048), false)),
		Elsewhere:  decisionField(o, "elsewhere", false, decisionPointer(decisionNested(readDecisionElsewhere))),
	}
}

func readDecisionElsewhere(o decisionObject) DecisionElsewhere {
	return DecisionElsewhere{
		Channel: decisionField(o, "channel", true, checkedString(atMostChars(64), false)),
		Decider: decisionField(o, "decider", true, decisionNested(readDecisionActor)),
		Receipt: decisionField(o, "receipt", false, decisionNested(readDecisionReceipt)),
	}
}
