// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Remedies, one per decline cause (MTIX-112.19.8). Each git command names the
// checkout root the predicate matched (git -C), so it behaves the same from any
// directory and from a linked worktree. The tracked-store one also drops a split
// index, because a split index's shared base keeps listing the store after
// git rm --cached. All were run against real git.
const (
	sparseIndexFix = "sparse-checkout reapply --no-sparse-index"
	unsupportedFix = "upgrade mtix (mtix version shows this build): it cannot read this git index layout, and glue stays off in this checkout until it can"
)

// shellQuote single-quotes s for a POSIX shell, so a path or an id read from
// state never becomes shell syntax in a printed command.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// gitCmd is the git invocation for the matched checkout.
func gitCmd(d attach.GlueDecision) string {
	if d.CheckoutRoot == "" {
		return "git"
	}
	return "git -C " + shellQuote(d.CheckoutRoot)
}

// storeDirRel is the matched store's directory from the checkout root, as a
// slash path with a trailing slash ("" when the store sits at the root).
func storeDirRel(d attach.GlueDecision) string {
	if d.CheckoutRoot == "" || d.StoreRoot == "" {
		return ""
	}
	rel, err := filepath.Rel(d.CheckoutRoot, d.StoreRoot)
	if err != nil || rel == "." {
		return ""
	}
	return path.Clean(filepath.ToSlash(rel)) + "/"
}

func trackedStoreFix(d attach.GlueDecision) string {
	data := storeDirRel(d) + ".mtix/data"
	ignore := filepath.Join(d.CheckoutRoot, ".gitignore")
	return fmt.Sprintf("%[1]s rm -r --cached --ignore-unmatch -- %[2]s && %[1]s update-index --no-split-index && echo %[3]s >> %[4]s",
		gitCmd(d), shellQuote(data), shellQuote(data+"/"), shellQuote(ignore))
}

func trackedAncestorFix(d attach.GlueDecision) string {
	prefix := storeDirRel(d)
	return fmt.Sprintf("%[1]s update-index --force-remove -- %[2]s %[3]s && %[1]s update-index --no-split-index",
		gitCmd(d), shellQuote(prefix+".mtix"), shellQuote(prefix+".mtix/data"))
}

func splitBaseFix(d attach.GlueDecision) string {
	return gitCmd(d) + " read-tree HEAD   # rebuilds the index from HEAD; commit or stash staged changes first"
}

func pathLengthFix(d attach.GlueDecision) string {
	return gitCmd(d) + " ls-files | awk 'length($0) > 4096'   # lists the paths mtix does not read; git rm --cached or rename them, then run mtix doctor again"
}

// glueDeclineFix maps a decline (reason and cause) to its detail and remedy; ok
// is false for reasons that are not a problem to report.
func glueDeclineFix(d attach.GlueDecision) (detail, fix string, ok bool) {
	switch d.Reason {
	case attach.ReasonTrackedStore:
		if d.Cause == attach.TrackedAncestor {
			return "git tracks a symlink or submodule entry at .mtix or .mtix/data, so harness glue does nothing in this checkout", trackedAncestorFix(d), true
		}
		return "the board store is tracked by git, so harness glue does nothing in this checkout", trackedStoreFix(d), true
	case attach.ReasonIndexUnsupported:
		return indexCauseFix(d)
	}
	return "", "", false
}

func indexCauseFix(d attach.GlueDecision) (detail, fix string, ok bool) {
	switch d.Cause {
	case attach.CauseSparse:
		return "the git index is a sparse index with a directory entry covering .mtix, which mtix refuses to guess about, so harness glue does nothing in this checkout", gitCmd(d) + " " + sparseIndexFix, true
	case attach.CauseSplitBase:
		return "the git index is a split index whose shared base file is missing, so harness glue does nothing in this checkout", splitBaseFix(d), true
	case attach.CauseOversize:
		return "the git index is larger than the size mtix reads, so harness glue does nothing in this checkout", unsupportedFix, true
	case attach.CausePathLength:
		return fmt.Sprintf("the git index holds a path longer than the %d bytes mtix reads, so harness glue does nothing in this checkout", attach.MaxPathBytes), pathLengthFix(d), true
	case attach.CauseExtension, attach.CauseVersion:
		return "the git index uses a version or extension this mtix does not know, so harness glue does nothing in this checkout", unsupportedFix, true
	}
	return "the git index could not be parsed (for example a SHA-256 repository), so harness glue does nothing in this checkout", unsupportedFix, true
}

var (
	queuedAgentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	queuedHarness = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// queuedLane is the registration a queued attach row recorded.
type queuedLane struct {
	AgentID  string `json:"agent_id"`
	Harness  string `json:"harness"`
	LanePath string `json:"lane_path"`
}

// strandedRegisterFix builds the exact command that lists the board from a
// normal shell and keeps each queued registration as it was: its agent id, its
// harness (a bare register would record the harness as unknown) and the lane
// directory it registered from. Rows are untrusted state: a field that is not a
// plain id or an absolute path is skipped, and every value is quoted.
func strandedRegisterFix(rows []sqlite.AttachPending) string {
	seen := map[queuedLane]bool{}
	var cmds []string
	for _, row := range rows {
		var q queuedLane
		if json.Unmarshal([]byte(row.Payload), &q) != nil || seen[q] {
			continue
		}
		seen[q] = true
		if !queuedAgentID.MatchString(q.AgentID) || !filepath.IsAbs(q.LanePath) || filepath.Clean(q.LanePath) != q.LanePath {
			continue
		}
		cmd := "cd " + shellQuote(q.LanePath) + " && mtix agent register " + shellQuote(q.AgentID)
		if queuedHarness.MatchString(q.Harness) && q.Harness != "unknown" {
			cmd += " --harness " + shellQuote(q.Harness)
		}
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 0 {
		return "mtix agent register <agent-id> --harness <harness>   # run it for the agent that registered, from a normal shell in this checkout (outside any sandbox), so the host lists the board"
	}
	return strings.Join(cmds, " ; ") + "   # run from a normal shell outside any sandbox, so the host lists the board; add --session <handle> if the session was registered with one"
}
