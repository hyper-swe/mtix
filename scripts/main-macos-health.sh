#!/usr/bin/env bash
# MTIX-112.17.42: block merges while main's macOS CI is red.
#
# Pull requests run one macOS job; the full macOS matrix runs on push to main
# and nightly. This check judges the newest informative main run and fails
# unless every expected macOS job (scripts/main-macos-expected-jobs.txt, the
# single list) ran and succeeded.
#
# Two places run it:
#   1. The test-go job on pull_request, from the BASE branch's copy of this
#      script. It is advisory for a hostile PR, which can edit ci.yml itself.
#   2. The merger, standalone with gh credentials, immediately before every
#      merge (enforcing; the required-check ruleset is not strict). The merger
#      runs the copy on origin/main as fetched at merge time, never the PR
#      branch's copy: the exact command is in docs/CI-GATES.md. A red, unknown
#      or failing result stops the merge.
#
# Policy, newest push or schedule run of ci.yml on main first. Jobs are read
# per run attempt (actions/runs/ID/attempts/N/jobs), de-duplicated by job name
# to the highest run_attempt:
#   - latest attempt completed, not cancelled: judge it. Any expected job
#     missing, or any macOS job not success (skipped included), is red.
#   - latest attempt cancelled, or a re-run in progress (attempt above 1):
#     judge the previous attempt, walking down past attempts that were
#     themselves cancelled. A red earlier attempt stays red; if no attempt has
#     a result, fail closed.
#   - first attempt cancelled: red if a macOS job failed or timed out;
#     otherwise skipped, for at most 3 runs, then fail closed.
#   - first attempt in progress: ignored (accepted residual: a run in
#     progress is not red; the next merge after it finishes sees the result).
#   - API error, unreadable data, no run to judge: fail closed.
# Override: PR label main-macos-fix (PR_LABELS is a JSON array).
# Takes no arguments. Needs gh, jq, GH_TOKEN and GITHUB_REPOSITORY.
set -uo pipefail

OVERRIDE_LABEL="main-macos-fix"
MAX_CANCELLED_SKIPS=3
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd) || exit 1
tmp=$(mktemp -d) || { echo "main-macos-health: cannot create a temporary directory" >&2; exit 1; }
trap 'rm -f "$tmp/runs.json" "$tmp/jobs.raw" "$tmp/jobs.json" "$tmp/err" "$tmp/ids"; rmdir "$tmp"' EXIT

fail() {
  echo "main-macos-health: $*" >&2
  [ -s "$tmp/err" ] && sed 's/^/main-macos-health: gh: /' "$tmp/err" >&2
  exit 1
}

[ $# -eq 0 ] || fail "takes no arguments (got: $*)"

if jq -e --arg l "$OVERRIDE_LABEL" 'type == "array" and index($l) != null' <<<"${PR_LABELS:-[]}" >/dev/null 2>&1; then
  echo "main-macos-health: override: label $OVERRIDE_LABEL is set; the main macOS state is not enforced"
  exit 0
fi
[ -n "${GITHUB_REPOSITORY:-}" ] || fail "cannot read main CI state: GITHUB_REPOSITORY is unset"
expected_file="$here/main-macos-expected-jobs.txt"
[ -s "$expected_file" ] || fail "cannot read the expected macOS job list $expected_file"

# fetch_jobs RUN_ID ATTEMPT: the jobs of one run attempt, de-duplicated by job
# name to the highest run_attempt, one JSON object per line in jobs.json.
fetch_jobs() {
  gh api --paginate "repos/${GITHUB_REPOSITORY}/actions/runs/$1/attempts/$2/jobs?per_page=100" --jq '.jobs[]' \
    >"$tmp/jobs.raw" 2>"$tmp/err" || fail "cannot read jobs of main run $1 attempt $2 (API error); failing closed"
  jq -s -e 'length > 0 and all(type == "object" and (.name | type == "string"))' "$tmp/jobs.raw" >/dev/null 2>&1 \
    || fail "cannot read jobs of main run $1 attempt $2 (unparsable or empty job list); failing closed"
  jq -c -s 'group_by(.name) | map(max_by(.run_attempt // 1)) | .[]' "$tmp/jobs.raw" >"$tmp/jobs.json" \
    || fail "cannot read jobs of main run $1 attempt $2; failing closed"
}

# macos_names FILTER: comma-separated macOS jobs of jobs.json selected by a jq condition.
macos_names() {
  jq -s -r "[.[] | select(.name | ascii_downcase | contains(\"macos\")) | select($1)]
    | map(\"\\(.name)=\\(.conclusion // \"unknown\")\") | join(\", \")" "$tmp/jobs.json" \
    || exit 1
}

# attempt_state RUN_ID LABEL: sets STATE (green, red or incomplete) and REASON
# for the jobs in jobs.json. A failed or timed out macOS job is red; a
# cancelled or unfinished macOS job leaves the attempt without a result.
attempt_state() {
  local unfinished missing bad
  FAILED=$(macos_names '.conclusion == "failure" or .conclusion == "timed_out"') || fail "cannot evaluate main run $1; failing closed"
  unfinished=$(macos_names '.conclusion == "cancelled" or .conclusion == null') || fail "cannot evaluate main run $1; failing closed"
  missing=$(jq -s -r --rawfile exp "$expected_file" \
    '(($exp | split("\n") | map(select(length > 0))) - [.[].name]) | join(", ")' "$tmp/jobs.json") \
    || fail "cannot evaluate main run $1; failing closed"
  bad=$(macos_names '.conclusion != "success"') || fail "cannot evaluate main run $1; failing closed"
  if [ -n "$FAILED" ]; then STATE=red; REASON="main is red on macOS in run $1 ($2): $FAILED"
  elif [ -n "$unfinished" ]; then STATE=incomplete; REASON="run $1 ($2) has unfinished macOS jobs: $unfinished"
  elif [ -n "$missing" ]; then STATE=red; REASON="main run $1 ($2) is missing expected macOS jobs: $missing"
  elif [ -n "$bad" ]; then STATE=red; REASON="main is red on macOS in run $1 ($2): $bad"
  else STATE=green; REASON="judged main run $1 ($2): all expected macOS jobs succeeded"; fi
}

red_exit() { fail "$REASON. Fix main first, or label the repairing PR $OVERRIDE_LABEL."; }

# judge_attempt RUN_ID ATTEMPT LABEL: a final verdict for a finished attempt.
judge_attempt() {
  fetch_jobs "$1" "$2"; attempt_state "$1" "$3"
  [ "$STATE" = green ] || { [ "$STATE" != incomplete ] || REASON="$REASON; no result"; red_exit; }
  echo "main-macos-health: $REASON"
  exit 0
}

# judge_earlier RUN_ID ATTEMPT: the latest attempt has no result; walk down to
# the newest earlier attempt that has one. Red stays red.
judge_earlier() {
  local k
  for ((k = $2 - 1; k >= 1; k--)); do
    fetch_jobs "$1" "$k"; attempt_state "$1" "attempt $k of a re-run"
    case "$STATE" in
      green) echo "main-macos-health: $REASON"; exit 0 ;;
      red) red_exit ;;
    esac
  done
  fail "main run $1 has no attempt with a result (all cancelled or unfinished); failing closed"
}

gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/ci.yml/runs?branch=main&per_page=30" \
  >"$tmp/runs.json" 2>"$tmp/err" || fail "cannot read main CI state (API error); failing closed"
jq -s -e 'length == 1 and (.[0] | type == "object") and (.[0].workflow_runs | type == "array")' "$tmp/runs.json" >/dev/null 2>&1 \
  || fail "cannot read main CI state (unparsable or empty run list); failing closed"
# One line per push or schedule run, newest first: id, status, conclusion, attempt.
# Empty fields are written as "-" because read collapses adjacent tabs.
jq -r '.workflow_runs[] | select(.event == "push" or .event == "schedule")
  | [(.id | tostring), (.status // "-"), (.conclusion // "-"), ((.run_attempt // 1) | tostring)] | @tsv' \
  "$tmp/runs.json" >"$tmp/ids" || fail "cannot read main CI state (unparsable run list); failing closed"

skipped=0
while IFS=$'\t' read -r id status conclusion attempt; do
  case "$id$attempt" in ""|*[!0-9]*) fail "unparsable run entry '$id' '$attempt'; failing closed" ;; esac
  if [ "$status" = completed ] && [ "$conclusion" != cancelled ]; then
    judge_attempt "$id" "$attempt" "attempt $attempt"
  elif [ "$attempt" -gt 1 ]; then
    if [ "$status" = completed ]; then
      fetch_jobs "$id" "$attempt"; attempt_state "$id" "cancelled re-run"
      [ -z "$FAILED" ] || red_exit
    fi
    judge_earlier "$id" "$attempt"
  elif [ "$status" = completed ]; then
    fetch_jobs "$id" 1; attempt_state "$id" "cancelled run"
    [ -z "$FAILED" ] || red_exit
    skipped=$((skipped + 1))
    [ "$skipped" -le "$MAX_CANCELLED_SKIPS" ] || fail "more than $MAX_CANCELLED_SKIPS consecutive cancelled main runs; no result to judge; failing closed"
  else
    echo "main-macos-health: main run $id is in progress and is not judged (a run in progress is not red)"
  fi
done <"$tmp/ids"
fail "no completed main CI run (push or schedule) to judge; failing closed"
