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
#      merge (enforcing; the required-check ruleset is not strict):
#        GITHUB_REPOSITORY=<owner>/<repo> PR_LABELS="$(gh pr view <n> --json labels --jq '[.labels[].name]')" \
#          bash scripts/main-macos-health.sh
#      A red, unknown or failing result stops the merge.
#
# Policy (walk completed-or-running push and schedule runs of ci.yml on main,
# newest first):
#   - completed, not cancelled: judge it. Any expected job missing, or any
#     macOS job not success (skipped included), is red.
#   - completed and cancelled: red if a macOS job in its latest attempt
#     failed or timed out; otherwise skipped, for at most 3 runs, then fail
#     closed.
#   - not completed, first attempt: ignored (accepted residual: a run in
#     progress is not red; the next merge after it finishes sees the result).
#   - not completed, run_attempt above 1 (a re-run): judge the previous
#     attempt; fail closed unless it was green.
#   - API error, unreadable data, no run to judge: fail closed.
# Override: PR label main-macos-fix (PR_LABELS is a JSON array).
# Takes no arguments. Needs gh, jq, GH_TOKEN and GITHUB_REPOSITORY.
set -uo pipefail

OVERRIDE_LABEL="main-macos-fix"
MAX_CANCELLED_SKIPS=3
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd) || exit 1
tmp=$(mktemp -d) || { echo "main-macos-health: cannot create a temporary directory" >&2; exit 1; }
trap 'rm -f "$tmp/runs.json" "$tmp/jobs.json" "$tmp/err" "$tmp/ids"; rmdir "$tmp"' EXIT

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

# fetch_jobs RUN_ID [ATTEMPT]: all jobs of that run attempt, one JSON object per line.
fetch_jobs() {
  local path="actions/runs/$1/jobs?per_page=100&filter=latest"
  [ -z "${2:-}" ] || path="actions/runs/$1/attempts/$2/jobs?per_page=100"
  gh api --paginate "repos/${GITHUB_REPOSITORY}/${path}" --jq '.jobs[]' >"$tmp/jobs.json" 2>"$tmp/err" \
    || fail "cannot read jobs of main run $1 (API error); failing closed"
  jq -s -e 'length > 0 and all(type == "object" and (.name | type == "string"))' "$tmp/jobs.json" >/dev/null 2>&1 \
    || fail "cannot read jobs of main run $1 (unparsable or empty job list); failing closed"
}

# judge_full RUN_ID LABEL: every expected macOS job present and success, no other macOS job red.
judge_full() {
  local missing bad
  missing=$(jq -s -r --rawfile exp "$expected_file" \
    '(($exp | split("\n") | map(select(length > 0))) - [.[].name]) | join(", ")' "$tmp/jobs.json") \
    || fail "cannot evaluate main run $1; failing closed"
  bad=$(jq -s -r '[.[] | select(.name | ascii_downcase | contains("macos")) | select(.conclusion != "success")]
    | map("\(.name)=\(.conclusion // "unknown")") | join(", ")' "$tmp/jobs.json") \
    || fail "cannot evaluate main run $1; failing closed"
  [ -z "$missing" ] || fail "main run $1 ($2) is missing expected macOS jobs: $missing. Fix main first, or label the repairing PR $OVERRIDE_LABEL."
  [ -z "$bad" ] || fail "main is red on macOS in run $1 ($2): $bad. Fix main first, or label the repairing PR $OVERRIDE_LABEL."
  echo "main-macos-health: judged main run $1 ($2): all expected macOS jobs succeeded"
  exit 0
}

# failed_in_cancelled RUN_ID: fail if a macOS job of the cancelled run failed or timed out.
failed_in_cancelled() {
  local bad
  bad=$(jq -s -r '[.[] | select(.name | ascii_downcase | contains("macos")) | select(.conclusion == "failure" or .conclusion == "timed_out")]
    | map("\(.name)=\(.conclusion)") | join(", ")' "$tmp/jobs.json") || fail "cannot evaluate cancelled main run $1; failing closed"
  [ -z "$bad" ] || fail "main is red on macOS in cancelled run $1: $bad. Fix main first, or label the repairing PR $OVERRIDE_LABEL."
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
  if [ "$status" = completed ] && [ "$conclusion" = cancelled ]; then
    fetch_jobs "$id"; failed_in_cancelled "$id"
    skipped=$((skipped + 1))
    [ "$skipped" -le "$MAX_CANCELLED_SKIPS" ] || fail "more than $MAX_CANCELLED_SKIPS consecutive cancelled main runs; no result to judge; failing closed"
  elif [ "$status" = completed ]; then
    fetch_jobs "$id"; judge_full "$id" "attempt $attempt"
  elif [ "$attempt" -gt 1 ]; then
    fetch_jobs "$id" "$((attempt - 1))"; judge_full "$id" "re-run in progress, judging attempt $((attempt - 1))"
  else
    echo "main-macos-health: main run $id is in progress and is not judged (a run in progress is not red)"
  fi
done <"$tmp/ids"
fail "no completed main CI run (push or schedule) to judge; failing closed"
