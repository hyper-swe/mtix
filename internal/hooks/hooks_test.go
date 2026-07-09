// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatches_AndComposedFilters(t *testing.T) {
	h := Hook{
		Name: "wake-worker",
		Match: Match{
			Events:       []string{EventStatusChanged},
			FromAgentNot: "opus",
			Under:        "HP-1",
			StatusTo:     []string{"done", "blocked"},
		},
		Deliver: []string{AdapterInbox},
	}
	base := Event{Name: EventStatusChanged, NodeID: "HP-1.2", Author: "worker", StatusTo: "done"}

	require.True(t, h.Matches(base), "all filters satisfied")
	require.False(t, h.Matches(mut(base, func(e *Event) { e.Name = EventNodeCreated })), "wrong event")
	require.False(t, h.Matches(mut(base, func(e *Event) { e.Author = "opus" })), "from-agent-not excludes self")
	require.False(t, h.Matches(mut(base, func(e *Event) { e.NodeID = "HP-2.1" })), "outside subtree")
	require.False(t, h.Matches(mut(base, func(e *Event) { e.StatusTo = "open" })), "status not in set")
	require.False(t, h.Matches(mut(base, func(e *Event) { e.Synced = true })), "synced without include-synced")
	require.True(t, h.Matches(mut(base, func(e *Event) { e.NodeID = "HP-1" })), "ancestor itself is in-subtree")
}

func TestMatches_ToAgentFiltersOnlyAddressedComments(t *testing.T) {
	h := Hook{Match: Match{Events: []string{EventCommentAddressed, EventStatusChanged}, ToAgent: "opus"}, Deliver: []string{AdapterInbox}}
	require.True(t, h.Matches(Event{Name: EventCommentAddressed, ToAgent: "opus"}), "addressed to opus")
	require.False(t, h.Matches(Event{Name: EventCommentAddressed, ToAgent: "sonnet"}), "addressed to someone else")
	// status.changed has no addressee — to-agent is the delivery target, not a filter.
	require.True(t, h.Matches(Event{Name: EventStatusChanged, NodeID: "X-1"}), "status change matches; to-agent is the target")
}

func TestMatches_EmptyFiltersAreWildcards(t *testing.T) {
	h := Hook{Name: "broad", Match: Match{Events: []string{EventNodeCreated}}, Deliver: []string{AdapterInbox}}
	require.True(t, h.Matches(Event{Name: EventNodeCreated, NodeID: "ANY-9", Author: "whoever"}))
}

func TestUnderSubtree_NoSiblingPrefixFalsePositive(t *testing.T) {
	require.True(t, underSubtree("HP-1", "HP-1"))
	require.True(t, underSubtree("HP-1", "HP-1.2.3"))
	require.False(t, underSubtree("HP-1", "HP-12"), "HP-12 is a sibling, not a descendant")
}

func TestLoad_MissingFile_EmptyNoWarnings(t *testing.T) {
	cfg, warns := Load(t.TempDir())
	require.Empty(t, cfg.Hooks)
	require.Empty(t, warns)
}

func TestLoad_ValidAndInvalidHooks(t *testing.T) {
	dir := t.TempDir()
	yaml := `
hooks:
  - name: good
    match:
      events: [comment.addressed]
    deliver: [inbox]
  - name: bad-event
    match:
      events: [not.a.real.event]
    deliver: [inbox]
  - name: exec-missing-command
    match:
      events: [status.changed]
    deliver: [exec]
  - name: ""
    match:
      events: [node.created]
    deliver: [inbox]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(yaml), 0o600))

	cfg, warns := Load(dir)
	require.Len(t, cfg.Hooks, 1, "only the valid hook survives")
	require.Equal(t, "good", cfg.Hooks[0].Name)
	require.Len(t, warns, 3, "three bad hooks each warn, none fail the load")
}

func mut(e Event, f func(*Event)) Event { f(&e); return e }

func TestMatches_ViaHookNeverRetriggersSameHook(t *testing.T) {
	h := Hook{Name: "h", Match: Match{Events: []string{EventStatusChanged}}, Deliver: []string{AdapterInbox}}
	require.True(t, h.Matches(Event{Name: EventStatusChanged}), "an ordinary event matches")
	require.False(t, h.Matches(Event{Name: EventStatusChanged, ViaHook: "h"}), "the hook's own exec output does not re-trigger it")
	require.True(t, h.Matches(Event{Name: EventStatusChanged, ViaHook: "other"}), "another hook's output still matches")
}
