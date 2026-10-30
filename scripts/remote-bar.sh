#!/usr/bin/env bash
# remote-bar.sh - run a tree's committed bar on GitHub-hosted runners (MTIX-167).
#
# Lab tooling for the review loop. Nothing here ships to customers.
#
# Usage:
#   scripts/remote-bar.sh [--ticket MTIX-N] [--dry-run] [--allow-workflow-diff] \
#                         [--no-reuse] \
#                         [--os ubuntu-22.04|macos-latest] [--web] \
#                         <worktree-or-checkout>
#   scripts/remote-bar.sh --help
#
# What it does:
#   1. Snapshots <worktree-or-checkout> into a commit whose parent is
#      origin/main, on a throwaway branch ci-scratch/<ticket>-<utc>-<tree8>.
#      A temporary index is used; the caller's index, HEAD and files are
#      never touched. A git-less HyperSWE loop worktree is fine.
#   2. Pushes ONLY refs/heads/ci-scratch/* to hyper-swe/mtix. Any other push
#      target, or any other remote URL (fetch or push), is refused. Tags are
#      never pushed, whatever the caller's push config says.
#      A snapshot whose .github subtree differs from origin/main is refused,
#      because GitHub runs push-event workflows from the pushed commit; pass
#      --allow-workflow-diff to accept that knowingly (a warning is printed).
#   A changed remote-bar.yml is always refused, even with the opt-in flag.
#   Every verdict names its actual workflow_ref and workflow_sha.
#   3. Dispatches .github/workflows/remote-bar.yml from main (a workflow can
#      be dispatched only when its file is on the default branch) with the
#      scratch branch and the expected tree hash as inputs. The job checks
#      the tree hash first and fails on a mismatch.
#   4. Waits for the run, prints the result and the failing log, and deletes
#      the scratch branch on every exit path (success, failure, interrupt).
#
# The bar is the committed one: make lint-go && make test-race, then
# govulncheck. On Linux the race tests run in parallel jobs that together cover
# exactly what make test-race covers: the PostgreSQL packages serially, the
# other packages with -p 4, internal/store/sqlite and internal/service as hash
# shards (scripts/ci-go-shards.sh bar-check and test-check,
# MTIX-112.17.27) and three cmd/mtix shards (scripts/cmd-mtix-shards.sh,
# MTIX-112.17.24). By default it runs on ubuntu-22.04 with PostgreSQL 16 (CI also
# covers 17). Options (MTIX-203; they take effect once the workflow carrying
# the matching inputs is on main):
#   --os macos-latest   run the Go bar on a macOS runner instead. Allowlist:
#                       ubuntu-22.04 (default) and macos-latest; anything else
#                       exits 2. macOS runners have no PostgreSQL service, so
#                       the PG-gated suites skip there.
#   --web               also run the web job: npm ci, npm audit (dev
#                       dependencies included, any advisory fails), lint,
#                       tsc -b, vitest, build, then a byte compare of the
#                       rebuilt bundle against internal/web/dist.
# Default runs send no new dispatch inputs, so they work against a workflow
# on main that predates the options. Windows coverage comes from PR CI.
#
# Reuse (MTIX-112.17.26): before dispatching, the wrapper looks for a completed
# successful remote-bar run of the same snapshot tree, main base commit, os, web option and
# wrapper version (remote-bar.yml puts them in the run-name), at most
# RB_REUSE_MAX_AGE_SECS old (default 12 hours), whose workflow file blob equals
# main's. If one exists it prints "REUSED run: <url> tree=... os=... web=..."
# and "verdict=REUSED", exits 0, and pushes and dispatches nothing. Failed,
# cancelled, queued and in-progress runs are never reused. --no-reuse forces a
# fresh run; a snapshot with --allow-workflow-diff never reuses.
#
# Needs: git, gh with existing auth (scopes repo and workflow). No secrets.
#
# Exit codes:
#   0  bar green
#   1  bar red (lint, race tests or govulncheck failed)
#   2  infra or dispatch failure (usage, remote refused, push, dispatch,
#      run lost, cancelled, timed out, runner setup failed)
#   3  tree mismatch (the runner's checkout differs from the snapshot)
#
# Environment: RB_REMOTE (default origin), RB_TIMEOUT_SECS (default 5400),
# RB_POLL_SECS (default 20), RB_FIND_SECS (default 5, wait between run lookups).

set -euo pipefail

RB_VERSION="2"
RB_OS_DEFAULT="ubuntu-22.04"
RB_OS_ALLOWED="ubuntu-22.04 macos-latest"
RB_REPO_SLUG="hyper-swe/mtix"
RB_WORKFLOW="remote-bar.yml"
RB_REMOTE="${RB_REMOTE:-origin}"
RB_TIMEOUT_SECS="${RB_TIMEOUT_SECS:-5400}"
RB_POLL_SECS="${RB_POLL_SECS:-20}"
RB_FIND_SECS="${RB_FIND_SECS:-5}"
RB_REUSE_MAX_AGE_SECS="${RB_REUSE_MAX_AGE_SECS:-43200}"
# Files the review loop injects at the worktree root. They are left out of the
# snapshot when origin/main does not track them, whatever the caller's config.
RB_LOOP_FILES="CLAUDE.md AGENTS.md"

RB_STEP_VERIFY="Verify snapshot tree"
RB_STEP_BAR="Run committed bar"
RB_STEP_VULN="Run govulncheck"
RB_STEP_WEB="Run web checks"
RB_STEP_CMD="Run cmd/mtix shard"
RB_STEP_CMD_GUARD="Verify exact cmd/mtix test coverage"
RB_STEP_SHARD="Run package shard"
RB_STEP_SHARD_GUARD="Verify exact package test coverage"
RB_STEP_SETS_GUARD="Verify exact package sets"

RB_BRANCH=""
RB_PUSHED=0
RB_RUN_ID=""
RB_TMPDIR=""
RB_NAP_PID=""
RB_CONCLUSION=""

RB_WORKFLOW_REF=not-run
RB_WORKFLOW_SHA=none
RB_REQUESTED_WORKFLOW_REF=none
RB_VERDICT_PRINTED=0
# shellcheck source=remote-bar-workflow.sh
source "$(dirname "${BASH_SOURCE[0]}")/remote-bar-workflow.sh"

# rb_remote_ok <url>: succeeds only for the hyper-swe/mtix GitHub repository.
rb_remote_ok() {
	local url="${1:-}"
	case "$url" in
	https://github.com/hyper-swe/mtix | https://github.com/hyper-swe/mtix.git | https://github.com/hyper-swe/mtix/) return 0 ;;
	git@github.com:hyper-swe/mtix | git@github.com:hyper-swe/mtix.git) return 0 ;;
	ssh://git@github.com/hyper-swe/mtix | ssh://git@github.com/hyper-swe/mtix.git) return 0 ;;
	esac
	return 1
}

# rb_remote_urls_ok <remote>: every fetch and push URL (after url rewrites)
# must be the hyper-swe/mtix repository, and there must be at least one.
rb_remote_urls_ok() {
	local remote="$1" url n=0
	while IFS= read -r url; do
		rb_remote_ok "$url" || return 1
		n=$((n + 1))
	done < <(git remote get-url "$remote" 2>/dev/null; git remote get-url --push --all "$remote" 2>/dev/null)
	[ "$n" -ge 2 ]
}

# rb_branch_name <ticket> <utc-stamp> <tree-hash>: prints the scratch branch.
rb_branch_name() {
	local ticket stamp tree
	ticket="$(printf '%s' "${1:-}" | tr -c 'A-Za-z0-9-' '-')"
	stamp="$(printf '%s' "${2:-}" | tr -c '0-9TZ' '-')"
	tree="${3:-}"
	[ -n "$ticket" ] && [ -n "$stamp" ] && [ "${#tree}" -ge 8 ] || return 1
	printf 'ci-scratch/%s-%s-%s\n' "$ticket" "$stamp" "${tree:0:8}"
}

# rb_assert_ref <ref>: succeeds only for refs/heads/ci-scratch/<safe name>.
rb_assert_ref() {
	local ref="${1:-}"
	case "$ref" in *..* | *//* | *+* | *:* | *' '*) return 1 ;; esac
	[[ "$ref" =~ ^refs/heads/ci-scratch/[A-Za-z0-9][A-Za-z0-9._-]*$ ]]
}

# rb_loop_files <parent>: prints each loop-injected root file that <parent>
# does not track. A file main tracks is a real change and stays.
rb_loop_files() {
	local f
	for f in $RB_LOOP_FILES; do
		if [ -z "$(git ls-tree --name-only "$1" -- "$f")" ]; then
			printf '%s\n' "$f"
		fi
	done
}

# rb_hidden_by_info_exclude <gitdir>: prints untracked entries that only the
# caller's info/exclude hides (not the worktree .gitignore, not known files).
# Run inside the snapshot environment, after the files were staged.
rb_hidden_by_info_exclude() {
	comm -23 \
		<(git ls-files -o -i --directory --exclude-from="$1/info/exclude" 2>/dev/null | sort) \
		<(git ls-files -o -i --directory --exclude-per-directory=.gitignore 2>/dev/null | sort) |
		grep -vxE '\.git/|\.hyperswe/|\.mgit/|CLAUDE\.md|AGENTS\.md' || true
}

# rb_snapshot_tree <dir> <parent>: prints the tree hash of <dir> using a
# temporary index seeded from <parent>, so a file tracked on main is never
# dropped by an ignore rule. .git, .hyperswe, .mgit and the loop-injected root
# files are left out explicitly; the worktree's own .gitignore is
# honoured and the caller's global excludes are not. The caller repo's
# info/exclude cannot be switched off, so files it alone hides are listed in
# a warning instead of being dropped silently.
rb_snapshot_tree() {
	local dir="$1" parent="$2" gitdir idx spec hidden loop_files f
	[ -d "$dir" ] || return 1
	dir="$(cd "$dir" && pwd -P)"
	gitdir="$(git rev-parse --absolute-git-dir)"
	idx="$RB_TMPDIR/index"
	loop_files="$(rb_loop_files "$parent")"
	(
		cd "$dir"
		export GIT_DIR="$gitdir" GIT_WORK_TREE="$dir" GIT_INDEX_FILE="$idx"
		git read-tree "$parent" >/dev/null
		# git add refuses the whole add (exit 1) when an exclude pathspec names
		# a path the worktree .gitignore already ignores (MTIX-183). Ignored
		# paths stay out of the add anyway, so only the others get a pathspec.
		spec=()
		for f in .git .hyperswe .mgit; do
			git -c core.excludesFile=/dev/null check-ignore -q --no-index -- "$f" ||
				spec+=(":(exclude)$f")
		done
		git -c core.excludesFile=/dev/null add -A -- . "${spec[@]}" >/dev/null || return 1
		# Loop-injected root files main does not track are dropped from the
		# index (not by pathspec, which makes git complain about ignored paths).
		for f in $loop_files; do
			git rm --cached -q --ignore-unmatch -- "$f" || return 1
		done
		hidden="$(rb_hidden_by_info_exclude "$gitdir" | tr '\n' ' ')"
		if [ -n "$hidden" ]; then
			echo "remote-bar: WARNING not in the snapshot, hidden only by this repository's info/exclude: $hidden" >&2
		fi
		git write-tree
	)
}

# rb_workflows_unchanged <parent> <tree>: succeeds when the .github subtree
# of <tree> equals the one of <parent>.
rb_workflows_unchanged() {
	git diff-tree -r --quiet "$1" "$2" -- .github
}

# rb_commit_tree <tree> <parent>: prints a commit of <tree> on <parent>.
rb_commit_tree() {
	GIT_AUTHOR_NAME="remote-bar" GIT_AUTHOR_EMAIL="remote-bar@invalid" \
		GIT_COMMITTER_NAME="remote-bar" GIT_COMMITTER_EMAIL="remote-bar@invalid" \
		git commit-tree "$1" -p "$2" -m "remote-bar snapshot $1"
}

# rb_push <commit> <branch>: pushes one scratch branch, never forced.
rb_push() {
	local commit="$1" branch="$2"
	rb_assert_ref "refs/heads/$branch" || rb_die 2 "refusing to push non ci-scratch target: $branch"
	RB_PUSHED=1
	git -c push.followTags=false push --quiet --no-follow-tags --no-verify "$RB_REMOTE" "$commit:refs/heads/$branch"
}

# rb_cleanup: deletes the scratch branch and temp files. Safe to call twice.
rb_cleanup() {
	if [ -n "$RB_NAP_PID" ]; then
		kill "$RB_NAP_PID" >/dev/null 2>&1 || true
		RB_NAP_PID=""
	fi
	if [ -n "$RB_RUN_ID" ] && [ "${RB_RUN_DONE:-0}" != 1 ]; then
		gh run cancel "$RB_RUN_ID" --repo "$RB_REPO_SLUG" >/dev/null 2>&1 || true
	fi
	if [ "$RB_PUSHED" = 1 ] && [ -n "$RB_BRANCH" ]; then
		if rb_assert_ref "refs/heads/$RB_BRANCH"; then
			git -c push.followTags=false push --quiet --no-follow-tags --no-verify "$RB_REMOTE" --delete "refs/heads/$RB_BRANCH" >/dev/null 2>&1 ||
				echo "remote-bar: WARNING could not delete $RB_BRANCH; delete it by hand" >&2
		fi
		RB_PUSHED=0
	fi
	if [ -n "$RB_TMPDIR" ] && [ -d "$RB_TMPDIR" ]; then
		rm -f "$RB_TMPDIR/index" "$RB_TMPDIR/index.lock" "$RB_TMPDIR/failed.log"
		rmdir "$RB_TMPDIR" 2>/dev/null || true
	fi
	RB_TMPDIR=""
}

# rb_classify <conclusion> <failed-steps joined by |>: prints the exit code.
rb_classify() {
	local conclusion="${1:-}" steps="${2:-}"
	case "$conclusion" in
	success) echo 0 ;;
	failure)
		case "|$steps|" in
		*"|$RB_STEP_VERIFY|"*) echo 3 ;;
		*"|$RB_STEP_BAR|"* | *"|$RB_STEP_VULN|"* | *"|$RB_STEP_WEB|"* | *"|$RB_STEP_CMD|"* | *"|$RB_STEP_CMD_GUARD|"* \
			| *"|$RB_STEP_SHARD|"* | *"|$RB_STEP_SHARD_GUARD|"* | *"|$RB_STEP_SETS_GUARD|"*) echo 1 ;;
		*) echo 2 ;;
		esac
		;;
	*) echo 2 ;;
	esac
}

# rb_nap <seconds>: sleeps in the background and waits, so a trapped signal
# fires at once instead of after the sleep.
rb_nap() {
	sleep "$1" &
	RB_NAP_PID=$!
	wait "$RB_NAP_PID" || true
	RB_NAP_PID=""
}

# rb_find_run <branch>: sets RB_RUN_ID to the dispatched run. Runs in the main
# shell so that signals are handled promptly.
rb_find_run() {
	local branch="$1" attempt id
	for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
		id="$(gh run list --repo "$RB_REPO_SLUG" --workflow "$RB_WORKFLOW" --event workflow_dispatch \
			--limit 30 --json databaseId,displayTitle \
			--jq ".[] | select(.displayTitle | contains(\"$branch\")) | .databaseId" 2>/dev/null | head -n 1 || true)"
		if [ -n "$id" ]; then
			RB_RUN_ID="$id"
			return 0
		fi
		[ "$attempt" -lt 12 ] && rb_nap "$RB_FIND_SECS"
	done
	return 1
}

# rb_wait_run <id>: sets RB_CONCLUSION (a conclusion, or timed_out). Returns 1
# when contact with the run is lost.
rb_wait_run() {
	local id="$1" start now state fails=0
	start="$(date +%s)"
	while :; do
		if state="$(gh run view "$id" --repo "$RB_REPO_SLUG" --json status,conclusion \
			--jq '.status + " " + (.conclusion // "")' 2>/dev/null)"; then
			fails=0
			case "$state" in "completed "*) RB_CONCLUSION="${state#completed }"; return 0 ;; esac
		else
			fails=$((fails + 1))
			[ "$fails" -lt 10 ] || return 1
		fi
		now="$(date +%s)"
		[ $((now - start)) -lt "$RB_TIMEOUT_SECS" ] || { RB_CONCLUSION=timed_out; return 0; }
		rb_nap "$RB_POLL_SECS"
	done
}

# rb_blob <ref>: prints the blob sha of the workflow file at <ref> (a branch or
# a commit sha), read from the GitHub API. Fails when it cannot be read.
rb_blob() {
	local sha
	sha="$(gh api "repos/$RB_REPO_SLUG/contents/.github/workflows/$RB_WORKFLOW?ref=$1" --jq .sha 2>/dev/null)" || return 1
	[ -n "$sha" ] && [ "$sha" != null ] || return 1
	printf '%s\n' "$sha"
}

# rb_find_reusable <tree> <base> <os> <web>: sets RB_REUSED to "<id> <url>" of a
# completed, successful remote-bar run whose WHOLE run-name (written by
# remote-bar.yml, whose validate job pins every field to a strict pattern)
# matches one anchored pattern and whose captured tree, base (the main commit
# the snapshot was cut from), os, web and wrapper equal the wanted values
# exactly. The run must be at most RB_REUSE_MAX_AGE_SECS old (default 12
# hours, because scans and floating images can turn red within days) and have
# run a workflow file whose blob equals main's now. Returns 1 when nothing
# qualifies or when any lookup fails (then a fresh run is dispatched). Queued,
# in-progress, failed and cancelled runs never qualify (MTIX-112.17.26).
rb_find_reusable() {
	local tree="$1" base="$2" os="$3" web="$4" main_blob id sha url re want
	re='^remote-bar ci-scratch/[A-Za-z0-9._-]+ tree=(?<t>[0-9a-f]{40}) base=(?<b>[0-9a-f]{40}) os=(?<o>ubuntu-22[.]04|macos-latest) web=(?<w>true|false) wrapper=(?<v>[0-9]+)$'
	want=".t == \"$tree\" and .b == \"$base\" and .o == \"$os\" and .w == \"$web\" and .v == \"$RB_VERSION\""
	RB_REUSED=""
	main_blob="$(rb_blob main)" || return 1
	while IFS=' ' read -r id sha url; do
		[ -n "$id" ] || continue
		[ "$(rb_blob "$sha" 2>/dev/null || true)" = "$main_blob" ] || continue
		RB_REUSED="$id $url"
		RB_WORKFLOW_REF=main
		RB_WORKFLOW_SHA="$sha"
		return 0
	done < <(gh run list --repo "$RB_REPO_SLUG" --workflow "$RB_WORKFLOW" --event workflow_dispatch \
		--status success --limit 100 --json databaseId,displayTitle,createdAt,headSha,headBranch,url,status,conclusion \
		--jq ".[] | select(.status == \"completed\" and .conclusion == \"success\" and .headBranch == \"main\" and ((.headSha // \"\") | test(\"^[0-9a-f]{40}$\")) and (now - (.createdAt | fromdateiso8601) <= $RB_REUSE_MAX_AGE_SECS) and ((.displayTitle | capture(\"$re\") // {}) | $want)) | \"\(.databaseId) \(.headSha) \(.url)\"" 2>/dev/null || true)
	return 1
}

# rb_os_ok <os>: succeeds only for a runner on the allowlist.
rb_os_ok() {
	local o
	for o in $RB_OS_ALLOWED; do
		[ "${1:-}" = "$o" ] && return 0
	done
	return 1
}

# rb_print_failed_log RUN_ID: prints a failed run's log. The log is captured
# once, then the failing-test lines of the WHOLE log come first (they often lie
# outside the tail), deduplicated by text without the runner's timestamp prefix and test duration
# and capped at 100, followed by the last 200 lines. MTIX-215.
rb_print_failed_log() {
	local logf="$RB_TMPDIR/failed.log"
	gh run view "$1" --repo "$RB_REPO_SLUG" --log-failed >"$logf" 2>&1 || true
	local marks='--- FAIL:|(^|[[:space:]])FAIL[[:space:]]+[^[:space:]]|panic:|\[build failed\]|test timed out'
	echo "failing tests:"
	{ grep -E -- "$marks" "$logf" || true; } |
		sed -E 's/^.*[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z[[:space:]]+//; s/[[:space:]]+\([0-9.]+s\)$//' |
		awk '!seen[$0]++' | head -n 100 || true
	echo "log tail (last 200 lines):"
	tail -n 200 "$logf" || true
}

rb_usage() {
	sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'
}

rb_run() {
	local ticket="adhoc" dry=0 allow_wf=0 reuse=1 dir="" a os="$RB_OS_DEFAULT" web=false
	while [ $# -gt 0 ]; do
		a="$1"
		case "$a" in
		-h | --help) rb_usage; return 0 ;;
		--ticket) [ $# -ge 2 ] || rb_die 2 "--ticket needs a value"; ticket="$2"; shift ;;
		--dry-run) dry=1 ;;
		--allow-workflow-diff) allow_wf=1 ;;
		--no-reuse) reuse=0 ;;
		--os) [ $# -ge 2 ] || rb_die 2 "--os needs a value"; os="$2"; shift
			rb_os_ok "$os" || rb_die 2 "--os must be one of: $RB_OS_ALLOWED (got: $os)" ;;
		--web) web=true ;;
		-*) rb_die 2 "unknown option: $a" ;;
		*) [ -z "$dir" ] || rb_die 2 "one directory only"; dir="$a" ;;
		esac
		shift
	done
	[[ "$RB_REUSE_MAX_AGE_SECS" =~ ^[1-9][0-9]*$ ]] || rb_die 2 "RB_REUSE_MAX_AGE_SECS must be a positive integer (got: $RB_REUSE_MAX_AGE_SECS)"
	[ -n "$dir" ] || rb_die 2 "missing <worktree-or-checkout>; see --help"
	git rev-parse --git-dir >/dev/null 2>&1 || rb_die 2 "run from inside a git checkout of mtix"

	RB_TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/remote-bar.XXXXXX")"
	local parent tree commit stamp
	if [ "$dry" = 0 ]; then
		rb_remote_urls_ok "$RB_REMOTE" || rb_die 2 "remote $RB_REMOTE (fetch or push URL) is not $RB_REPO_SLUG; refusing"
		git fetch --quiet "$RB_REMOTE" "refs/heads/main:refs/remotes/$RB_REMOTE/main" || rb_die 2 "fetch failed"
	fi
	parent="$(git rev-parse --verify "refs/remotes/$RB_REMOTE/main^{commit}" 2>/dev/null)" || rb_die 2 "no $RB_REMOTE/main"
	tree="$(rb_snapshot_tree "$dir" "$parent")" || rb_die 2 "snapshot failed for $dir"
	rb_bar_workflow_unchanged "$parent" "$tree" ||
		rb_die 2 "cannot judge a changed remote-bar.yml; no workflow ran (--allow-workflow-diff does not override this refusal)"
	if ! rb_workflows_unchanged "$parent" "$tree"; then
		[ "$allow_wf" = 1 ] || rb_die 2 "snapshot changes .github relative to $RB_REMOTE/main; pushing it would run unreviewed workflow code. Use --allow-workflow-diff only if that is intended"
		echo "remote-bar: WARNING snapshot changes .github relative to $RB_REMOTE/main (--allow-workflow-diff); main remote-bar.yml runs the tree, but changed workflows are not exercised" >&2
	fi
	stamp="$(date -u +%Y%m%dT%H%M%SZ)"
	RB_BRANCH="$(rb_branch_name "$ticket" "$stamp" "$tree")" || rb_die 2 "cannot name branch"
	echo "remote-bar $RB_VERSION: tree=$tree base=$parent branch=$RB_BRANCH os=$os web=$web"
	[ "$dry" = 0 ] || { echo "dry run: nothing pushed"; rb_verdict not_run none 0 DRY_RUN; return 0; }

	# A snapshot that changes .github never reuses: the workflow under change is
	# not the one a past run exercised (MTIX-112.17.26).
	if [ "$reuse" = 1 ] && [ "$allow_wf" = 0 ] && rb_find_reusable "$tree" "$parent" "$os" "$web"; then
		echo "REUSED run: https://github.com/$RB_REPO_SLUG/actions/runs/${RB_REUSED%% *} tree=$tree os=$os web=$web base=$parent wrapper=$RB_VERSION (--no-reuse forces a fresh run)"
		rb_verdict success none 0 REUSED
		return 0
	fi
	commit="$(rb_commit_tree "$tree" "$parent")"
	rb_push "$commit" "$RB_BRANCH" || rb_die 2 "push failed"
	# Optional inputs are sent only when asked for, so a default run still
	# dispatches against a workflow on main that predates them.
	local opts=()
	[ "$os" = "$RB_OS_DEFAULT" ] || opts+=(-f "os=$os")
	[ "$web" = false ] || opts+=(-f "web=true")
	# base_commit feeds the run-name used for reuse. A workflow on main that
	# predates it rejects the unknown input; only that rejection falls back to
	# a dispatch without it, and such a run is not reusable. Any other
	# dispatch error is a dispatch failure.
	local derr
	RB_REQUESTED_WORKFLOW_REF=main
	RB_WORKFLOW_REF=unknown
	RB_WORKFLOW_SHA=unknown
	if ! derr="$(gh workflow run "$RB_WORKFLOW" --repo "$RB_REPO_SLUG" --ref main \
		-f "scratch_ref=$RB_BRANCH" -f "expected_tree=$tree" -f "wrapper_version=$RB_VERSION" \
		-f "base_commit=$parent" "${opts[@]+"${opts[@]}"}" 2>&1)"; then
		case "$derr" in
		*"Unexpected inputs"*base_commit*)
			echo "remote-bar: main's workflow does not accept base_commit; dispatching without a base (this run is not reusable)" >&2
			gh workflow run "$RB_WORKFLOW" --repo "$RB_REPO_SLUG" --ref main \
				-f "scratch_ref=$RB_BRANCH" -f "expected_tree=$tree" -f "wrapper_version=$RB_VERSION" \
				"${opts[@]+"${opts[@]}"}" || rb_die 2 "dispatch failed" ;;
		*)
			printf '%s\n' "$derr" >&2
			rb_die 2 "dispatch failed" ;;
		esac
	fi
	rb_find_run "$RB_BRANCH" || rb_die 2 "dispatched run not found"
	rb_workflow_source || rb_die 2 "workflow source metadata is unavailable or contradicts requested ref main"
	echo "run: https://github.com/$RB_REPO_SLUG/actions/runs/$RB_RUN_ID"
	rb_wait_run "$RB_RUN_ID" || rb_die 2 "lost contact with the run"
	[ "$RB_CONCLUSION" = timed_out ] || RB_RUN_DONE=1
	local steps code
	steps="$(gh run view "$RB_RUN_ID" --repo "$RB_REPO_SLUG" --json jobs \
		--jq '[.jobs[].steps[] | select(.conclusion == "failure") | .name] | join("|")' 2>/dev/null || true)"
	code="$(rb_classify "$RB_CONCLUSION" "$steps")"
	rb_verdict "$RB_CONCLUSION" "$steps" "$code"
	if [ "$code" != 0 ]; then
		rb_print_failed_log "$RB_RUN_ID"
	fi
	return "$code"
}

rb_on_exit() {
	local status=$?
	trap - EXIT
	if [ "$status" != 0 ] && [ "$RB_VERDICT_PRINTED" != 1 ]; then rb_verdict error none "$status"; fi
	rb_cleanup
	exit "$status"
}

# rb_install_traps: cleanup runs on every exit path, including INT and TERM.
rb_install_traps() {
	trap rb_on_exit EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	rb_install_traps
	rb_run "$@"
fi
