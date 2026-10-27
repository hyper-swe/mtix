#!/usr/bin/env bash
# Copyright 2025-2026 HyperSWE
# SPDX-License-Identifier: Apache-2.0
# Workflow provenance and final verdicts for remote-bar.sh (MTIX-112.17.30).
# Sourced by the wrapper; no dispatch or other action happens on source.

rb_verdict() {
	local conclusion="$1" steps="$2" code="$3" kind="${4:-}" line
	if [ -n "$kind" ]; then
		line="conclusion=$conclusion verdict=$kind exit=$code"
	else
		line="conclusion=$conclusion failed_steps=${steps:-none} exit=$code"
	fi
	echo "$line workflow_ref=$RB_WORKFLOW_REF workflow_sha=$RB_WORKFLOW_SHA requested_workflow_ref=$RB_REQUESTED_WORKFLOW_REF"
	RB_VERDICT_PRINTED=1
}

rb_die() {
	local code="$1" conclusion=error
	shift
	echo "remote-bar: $*" >&2
	[ "$RB_WORKFLOW_REF" != not-run ] || conclusion=not_run
	rb_verdict "$conclusion" none "$code"
	exit "$code"
}

# Any difference includes additions, deletions and mode changes. The general
# .github opt-in never permits this wrapper to judge its own workflow change.
rb_bar_workflow_unchanged() {
	git diff-tree -r --quiet "$1" "$2" -- ".github/workflows/$RB_WORKFLOW"
}

# headBranch/headSha describe the workflow-dispatch source, not scratch_ref.
# A missing or contradictory response must never become a green result.
rb_workflow_source() {
	local source
	source="$(gh run view "$RB_RUN_ID" --repo "$RB_REPO_SLUG" --json headBranch,headSha \
		--jq '.headBranch + " " + .headSha' 2>/dev/null)" || return 1
	[[ "$source" =~ ^main\ [0-9a-f]{40}$ ]] || return 1
	RB_WORKFLOW_REF=main
	RB_WORKFLOW_SHA="${source#main }"
}
