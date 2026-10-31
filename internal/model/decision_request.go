// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"slices"
	"strconv"
)

// decodeRequest reads the request-specific fields of a decision object.
func decodeRequest(o *fieldObj) *DecisionRequest {
	r := &DecisionRequest{
		Question: o.str("question", strRule{required: true, minChars: 1, maxChars: maxQuestionChars, oneLine: true}),
		Context:  o.str("context", strRule{maxBytes: maxContextBytes}),
	}
	r.Options = decodeOptions(o)
	r.Recommend = decodeRecommend(o, r.Options)
	r.Links = decodeLinks(o)
	r.Deadline = o.timestamp("deadline", false)
	r.AnswerableBy = o.enum("answerable_by", DecisionAnswerableHuman, DecisionAnswerableHuman, DecisionAnswerableAny)
	r.Answerers = o.strings("answerers", maxAnswerers, strRule{minChars: 1, pattern: authorIDPattern})
	if hasDuplicates(r.Answerers) {
		o.c.fail(o.at("answerers"), "duplicate author id")
	}
	r.RequiresOutcome = o.boolean("requires_outcome", true)
	r.Key = o.str("key", strRule{minChars: 1, maxChars: maxKeyChars, pattern: decisionKeyPattern})
	r.Supersedes = o.strings("supersedes", maxSupersedes, strRule{minChars: 1, pattern: decisionEventID})
	if hasDuplicates(r.Supersedes) {
		o.c.fail(o.at("supersedes"), "duplicate request id")
	}
	r.Category = o.str("category", strRule{pattern: decisionCategory})
	r.Source = decodeSource(o)
	r.Asker = o.actor("asker", false, false, &DecisionActor{Kind: DecisionActorAgent})
	r.Ticket = decodeTicket(o)
	return r
}

func hasDuplicates(s []string) bool {
	seen := map[string]bool{}
	for _, v := range s {
		if seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}

func decodeOptions(o *fieldObj) []DecisionOption {
	out := []DecisionOption{}
	seen := map[string]bool{}
	for i, e := range o.array("options", false, maxOptions) {
		m, ok := e.(map[string]any)
		if !ok {
			o.c.fail(o.at("options"), "entry %d must be an object", i)
			continue
		}
		sub := &fieldObj{c: o.c, path: o.at("options") + "[" + strconv.Itoa(i) + "]", m: m}
		opt := DecisionOption{
			ID:           sub.str("id", strRule{required: true, pattern: decisionOptionID}),
			Label:        sub.str("label", strRule{maxChars: maxLabelChars}),
			Consequence:  sub.str("consequence", strRule{maxChars: maxConsequenceChars, oneLine: true}),
			NoteRequired: sub.boolean("note_required", false),
		}
		if in := sub.object("input", false); in != nil {
			opt.Input = &DecisionInput{
				Type: in.enum("type", "", DecisionInputText, DecisionInputFindingID,
					DecisionInputStoryRef, DecisionInputURL),
				Required: in.boolean("required", false),
			}
		}
		if seen[opt.ID] {
			o.c.fail(sub.at("id"), "duplicate option id")
		}
		seen[opt.ID] = true
		out = append(out, opt)
	}
	return out
}

func decodeRecommend(o *fieldObj, opts []DecisionOption) *DecisionRecommend {
	sub := o.object("recommend", false)
	if sub == nil {
		return nil
	}
	rec := &DecisionRecommend{
		Option: sub.str("option", strRule{required: true, pattern: decisionOptionID}),
		Reason: sub.str("reason", strRule{maxChars: maxReasonChars}),
	}
	if rec.Option != "" && !slices.ContainsFunc(opts, func(op DecisionOption) bool { return op.ID == rec.Option }) {
		o.c.fail(sub.at("option"), "does not name an option")
	}
	return rec
}

func decodeLinks(o *fieldObj) []DecisionLink {
	out := []DecisionLink{}
	for i, e := range o.array("links", false, maxLinks) {
		m, ok := e.(map[string]any)
		if !ok {
			o.c.fail(o.at("links"), "entry %d must be an object", i)
			continue
		}
		sub := &fieldObj{c: o.c, path: o.at("links") + "[" + strconv.Itoa(i) + "]", m: m}
		l := DecisionLink{
			Kind:             sub.str("kind", strRule{pattern: decisionDisplayEnum}),
			URL:              sub.str("url", strRule{required: true, maxBytes: maxLinkURLBytes}),
			Label:            sub.str("label", strRule{maxChars: maxLabelChars, oneLine: true}),
			Required:         sub.boolean("required", false),
			OpenBeforeAnswer: sub.boolean("open_before_answer", false),
		}
		if l.Kind == "" {
			l.Kind = "other"
		}
		if _, present := sub.m["url"]; present && sub.m["url"] != nil {
			if err := ValidateDecisionLinkURL(l.URL); err != nil {
				o.c.fail(sub.at("url"), "%v", err)
			}
		}
		out = append(out, l)
	}
	return out
}

func decodeSource(o *fieldObj) map[string]string {
	out := map[string]string{}
	sub := o.object("source", false)
	if sub == nil {
		return out
	}
	if len(sub.m) > maxSourcePairs {
		o.c.fail(sub.path, "more than %d pairs", maxSourcePairs)
		return out
	}
	for k := range sub.m {
		if !decisionSourceKey.MatchString(k) {
			o.c.fail(sub.path, "key %q does not match %s", clip(k), decisionSourceKey)
			continue
		}
		v, ok := sub.m[k].(string)
		if !ok {
			o.c.fail(sub.at(k), "must be a string")
			continue
		}
		sub.checkStr(k, v, strRule{maxChars: maxSourceValueChars, oneLine: true})
		out[k] = v
	}
	return out
}

func decodeTicket(o *fieldObj) DecisionTicket {
	var t DecisionTicket
	sub := o.object("ticket", true)
	if sub == nil {
		return t
	}
	uidRule := strRule{maxBytes: maxUIDBytes, oneLine: true}
	t.UID = sub.str("uid", uidRule)
	t.Ancestors = sub.strings("ancestors", maxAncestors, uidRule)
	t.Paths = sub.strings("paths", maxAncestors+1, strRule{maxBytes: maxPathBytes, oneLine: true})
	t.PathMatch = []bool{}
	for i, e := range sub.array("path_match", false, maxAncestors+1) {
		b, ok := e.(bool)
		if !ok {
			o.c.fail(sub.at("path_match"), "entry %d must be a boolean", i)
		}
		t.PathMatch = append(t.PathMatch, b)
	}
	t.Truncated = sub.boolean("truncated", false)
	if _, ok := sub.m["uid"].(string); !ok {
		o.c.fail(sub.at("uid"), "required")
	}
	want := 1 + len(t.Ancestors)
	if len(t.Paths) != want || len(t.PathMatch) != want {
		o.c.fail(sub.path, "paths and path_match must each hold %d entries, one for the uid and each ancestor", want)
	}
	return t
}
