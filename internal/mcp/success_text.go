// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
)

// The functions below render the plain-text success messages of tools that echo
// caller-supplied values. Every echoed value goes through format.SafeText, so a
// control, invisible or invalid byte in an id, agent id or reason cannot reach
// the agent as text (MTIX-187.7.8). Tools that return JSON do not use them.

func textPromptUpdated(id string) string {
	return fmt.Sprintf("Prompt updated for %s", format.SafeText(id))
}

func textAnnotationAdded(id, to string) string {
	if to == "" {
		return fmt.Sprintf("Annotation added to %s", format.SafeText(id))
	}
	return fmt.Sprintf("Annotation added to %s (→ %s inbox)", format.SafeText(id), format.SafeText(to))
}

// textAnnotationResolved takes the annotation id already rendered by
// format.EventID, because an annotation id is a pulled sync event id.
func textAnnotationResolved(renderedAnnotation, id string) string {
	return fmt.Sprintf("Annotation %s resolved on %s", renderedAnnotation, format.SafeText(id))
}

func textDepAdded(from, depType, to string) string {
	return fmt.Sprintf("Dependency added: %s -[%s]-> %s", format.SafeText(from), format.SafeText(depType), format.SafeText(to))
}

func textDepRemoved(from, depType, to string) string {
	return fmt.Sprintf("Dependency removed: %s -[%s]-> %s", format.SafeText(from), format.SafeText(depType), format.SafeText(to))
}

func textDeleted(id string) string   { return fmt.Sprintf("Deleted %s", format.SafeText(id)) }
func textUndeleted(id string) string { return fmt.Sprintf("Undeleted %s", format.SafeText(id)) }
func textUpdated(id string) string   { return fmt.Sprintf("Updated %s", format.SafeText(id)) }
func textInboxAcked(seq int64, agent string) string {
	return fmt.Sprintf("Acked inbox event %d for %s", seq, format.SafeText(agent))
}

func textSessionEnded(agent string) string {
	return fmt.Sprintf("Session ended for agent %s", format.SafeText(agent))
}

func textHeartbeat(agent string) string {
	return fmt.Sprintf("Heartbeat recorded for %s", format.SafeText(agent))
}

func textAgentState(agent, state string) string {
	return fmt.Sprintf("Agent %s state → %s", format.SafeText(agent), format.SafeText(state))
}

func textAgentNoWork(agent string) string {
	return fmt.Sprintf("Agent %s has no current work", format.SafeText(agent))
}

func textClaimed(id, agent string) string {
	return fmt.Sprintf("Claimed %s for %s", format.SafeText(id), format.SafeText(agent))
}

func textUnclaimed(id string) string { return fmt.Sprintf("Unclaimed %s", format.SafeText(id)) }
func textDone(id string) string      { return fmt.Sprintf("%s → done", format.SafeText(id)) }
func textReopened(id string) string  { return fmt.Sprintf("Reopened %s", format.SafeText(id)) }

func textDeferred(id, until string) string {
	if until == "" {
		return fmt.Sprintf("Deferred %s", format.SafeText(id))
	}
	return fmt.Sprintf("Deferred %s until %s", format.SafeText(id), until)
}

func textCancelled(id, reason string) string {
	return fmt.Sprintf("Cancelled %s: %s", format.SafeText(id), format.SafeText(reason))
}

func textRerun(id, strategy string) string {
	return fmt.Sprintf("Rerun %s with strategy %s", format.SafeText(id), format.SafeText(strategy))
}
