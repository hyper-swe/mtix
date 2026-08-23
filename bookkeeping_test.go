// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Bookkeeping gate per MTIX-74: a ticket whose work has merged must not
// still be open in the tracked task export.
//
// Bookkeeping lag is invisible by construction. Nothing fails, the code
// is right, and only the record is wrong — so it survives every test
// suite and every review, and surfaces when someone reads the export by
// eye. That is how MTIX-65 sat open for days after its fix merged, and
// the export is what a release report is assembled from.
//
// The rule keys on the repository's own commit convention rather than on
// a heuristic about wording: a work-landing commit names its ticket in
// the conventional-commit SCOPE, while prose mentions and
// ticket-creation commits do not.
package mtix_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scopeTicketPattern matches a ticket named in a commit's
// conventional-commit scope:
//
//	fix(backup)(MTIX-65): ...
//	feat(relay)(MTIX-64.3): ...
//	fix(MTIX-62): ...
//
// It deliberately does NOT match a ticket mentioned in prose after the
// colon. That distinction is the whole reason the gate holds at zero
// false positives: "chore: close MTIX-2.3.1 + open MTIX-2.3.2" CREATES
// a ticket rather than implementing it, and a gate that red-flagged
// ticket-creation commits would be re-run-and-ignored within a week.
var scopeTicketPattern = regexp.MustCompile(
	`^[a-z]+(?:\([^)]*\))*?\(\s*(MTIX-\d+(?:\.\d+)*)\s*\)[^:]*:`)

// ticketFromCommitSubject returns the ticket a commit landed work for,
// or "" when the subject names none in scope position.
func ticketFromCommitSubject(subject string) string {
	m := scopeTicketPattern.FindStringSubmatch(subject)
	if m == nil {
		return ""
	}
	return m[1]
}

// statusIsStale reports whether a ticket status means the record fell
// behind the code.
//
// Only "open" qualifies. "in_progress" is legitimate — an epic accrues
// commits while more work is coming — and "done" and "cancelled" are
// both settled. Choosing the tighter predicate is what keeps this gate
// worth reading when it fires.
func statusIsStale(status string) bool { return status == "open" }

// TestTicketFromCommitSubject pins the discriminator itself.
func TestTicketFromCommitSubject(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{"scope with a component", "fix(backup)(MTIX-65): refuse to verify a missing backup", "MTIX-65"},
		{"scope with a child ticket", "feat(relay)(MTIX-64.3): segment verdicts", "MTIX-64.3"},
		{"scope alone", "fix(MTIX-62): grant contents:read", "MTIX-62"},
		{"docs commit with a scope", "docs(MTIX-64.8): public documentation", "MTIX-64.8"},
		{"a ticket-creation commit", "chore: close MTIX-2.3.1 (streaming) + open MTIX-2.3.2 (JSONL)", ""},
		{"a prose mention", "chore: close MTIX-60 — v0.5.0-beta shipped", ""},
		{"a merge commit", "Merge branch 'fr21-surface'", ""},
		{"no ticket at all", "refactor(store): tidy the scan helpers", ""},
		{"a ticket only in the body position", "fix(store): something about MTIX-99", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ticketFromCommitSubject(tt.subject))
		})
	}
}

// TestStatusIsStale pins which statuses the gate treats as lag.
func TestStatusIsStale(t *testing.T) {
	require.True(t, statusIsStale("open"), "merged work under an unclaimed ticket is lag")
	require.False(t, statusIsStale("in_progress"), "an epic accrues commits while work continues")
	require.False(t, statusIsStale("done"))
	require.False(t, statusIsStale("cancelled"))
	require.False(t, statusIsStale("blocked"))
}

// TestBookkeeping_MergedWorkIsNotStillOpen is the gate.
func TestBookkeeping_MergedWorkIsNotStillOpen(t *testing.T) {
	requireFullGitHistory(t)

	status := loadTicketStatuses(t)
	var problems []string
	seen := map[string]bool{}
	for _, c := range commitsSinceBaseline(t) {
		ticket := ticketFromCommitSubject(c.subject)
		if ticket == "" || seen[ticket] {
			continue
		}
		st, known := status[ticket]
		if !known {
			// A ticket that is not in the export at all is a different
			// problem, and not this gate's to diagnose — the export may
			// legitimately be scoped to one project.
			continue
		}
		if statusIsStale(st) {
			// One line per ticket, naming the newest commit that landed
			// for it. Repeating a ticket once per commit buries the
			// count of real problems in noise.
			seen[ticket] = true
			problems = append(problems, fmt.Sprintf(
				"  %s is %q but %s landed its work\n    fix: mtix done %s",
				ticket, st, c.hash, ticket))
		}
	}
	require.Empty(t, problems, "work merged under a ticket nobody has claimed:\n%s\n\n"+
		"The code is right and only the record is wrong, which is exactly why nothing else\n"+
		"catches this. Close the ticket, or move it to in_progress if more work is coming.",
		strings.Join(problems, "\n"))
}

// commit is one commit's hash and subject.
type commit struct {
	hash    string
	subject string
}

// commitsSinceBaseline returns the commits a release would carry.
//
// Bounding the scan keeps the job fast and keeps a ticket settled long
// ago from resurrecting the gate. Set MTIX_BOOKKEEPING_FULL_HISTORY=1
// to audit the whole log instead.
func commitsSinceBaseline(t *testing.T) []commit {
	t.Helper()
	spec := "HEAD"
	if os.Getenv("MTIX_BOOKKEEPING_FULL_HISTORY") == "" {
		if tag := baselineTag(t); tag != "" {
			spec = tag + "..HEAD"
		}
	}
	out, err := exec.Command("git", "log", "--format=%h%x00%s", spec).Output()
	require.NoError(t, err, "read git log for %s", spec)

	var commits []commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		hash, subject, ok := strings.Cut(line, "\x00")
		if !ok {
			continue
		}
		commits = append(commits, commit{hash: hash, subject: subject})
	}
	return commits
}

// baselineTag returns the tag to measure this release against, or ""
// when there is none.
//
// The subtlety that makes this gate worth having at release time: a
// release is cut FROM a tag, and at a tagged commit `git describe`
// returns that same tag — so "since the last tag" would be an empty
// range and the gate would pass without examining a single commit. A
// release gate that is vacuous exactly when a release happens is the
// decorative kind. When HEAD is itself tagged, the baseline steps back
// to the tag before it, so the range is the release's own contents.
func baselineTag(t *testing.T) string {
	t.Helper()
	if err := exec.Command("git", "describe", "--tags", "--exact-match", "HEAD").Run(); err == nil {
		// HEAD is a release tag: measure against the previous one.
		out, err := exec.Command("git", "describe", "--tags", "--abbrev=0", "HEAD~1").Output()
		if err != nil {
			return "" // the first ever tag; scan everything behind it
		}
		return strings.TrimSpace(string(out))
	}
	out, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// requireFullGitHistory fails rather than skipping when the checkout is
// shallow.
//
// A gate that quietly no-ops in CI is worse than no gate: it reads as
// covered on every run while checking nothing. If this fires, the
// workflow needs fetch-depth: 0.
func requireFullGitHistory(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--is-shallow-repository").Output()
	require.NoError(t, err, "this gate needs a git checkout")
	require.Equal(t, "false", strings.TrimSpace(string(out)),
		"the checkout is shallow, so this gate can see almost no history and would "+
			"pass without checking anything. Give the job fetch-depth: 0.")
}

// loadTicketStatuses reads the tracked task export.
func loadTicketStatuses(t *testing.T) map[string]string {
	t.Helper()
	body, err := os.ReadFile(".mtix/tasks.json")
	require.NoError(t, err, "read the tracked task export")

	var export struct {
		Nodes []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(body, &export))
	require.NotEmpty(t, export.Nodes, "the task export is empty")

	out := make(map[string]string, len(export.Nodes))
	for _, n := range export.Nodes {
		out[n.ID] = n.Status
	}
	return out
}
