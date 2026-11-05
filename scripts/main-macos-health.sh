#!/usr/bin/env bash
# MTIX-112.17.42: block pull requests while main's macOS CI is red.
#
# Pull requests run only one macOS job; the full macOS matrix runs on push to
# main and nightly. This check, run by the test-go job on pull_request events
# only, reads the latest informative main run and fails when any macOS job in
# it did not succeed.
#
# Policy:
#   - Only completed ci.yml runs on main triggered by push or schedule count.
#   - A run whose overall conclusion is cancelled says nothing about health and
#     is skipped in favour of the next older one.
#   - A newer run still in progress is ignored: it neither clears nor causes a
#     block; the last completed run decides.
#   - Fail closed: an API error, unreadable data, no qualifying run, or a run
#     with no macOS jobs is a failure, never a pass.
#   - Override: the PR label main-macos-fix (applied by a maintainer, on the PR
#     that repairs main) turns the check into a notice.
#
# Usage: main-macos-health.sh [--runs RUNS.json --jobs JOBS.json]
# Live mode needs gh, GH_TOKEN and GITHUB_REPOSITORY. PR_LABELS is a JSON array.
set -uo pipefail

OVERRIDE_LABEL="main-macos-fix"
fail() { echo "main-macos-health: $*" >&2; exit 1; }

labels="${PR_LABELS:-[]}"
if jq -e --arg l "$OVERRIDE_LABEL" 'type == "array" and index($l) != null' <<<"$labels" >/dev/null 2>&1; then
  echo "main-macos-health: override: label $OVERRIDE_LABEL is set; the main macOS state is not enforced for this PR"
  exit 0
fi

runs_file="" jobs_file=""
while [ $# -gt 0 ]; do
  case "$1" in
    --runs) runs_file="${2:-}"; shift 2 ;;
    --jobs) jobs_file="${2:-}"; shift 2 ;;
    *) fail "unknown argument $1" ;;
  esac
done

tmp=$(mktemp -d) || fail "cannot create a temporary directory"
trap 'rm -f "$tmp"/runs.json "$tmp"/jobs.json; rmdir "$tmp"' EXIT

if [ -z "$runs_file" ]; then
  [ -n "${GITHUB_REPOSITORY:-}" ] || fail "cannot read main CI state: GITHUB_REPOSITORY is unset"
  # Latest ci.yml runs on main, newest first; run and event filtering is below.
  gh api "repos/${GITHUB_REPOSITORY}/actions/workflows/ci.yml/runs?branch=main&per_page=30" \
    >"$tmp/runs.json" 2>"$tmp/err" || fail "cannot read main CI state (API error); failing closed"
  runs_file="$tmp/runs.json"
fi

# The run list must be a single object holding a workflow_runs array. jq exits
# 0 with no output on an empty file in some versions, so check it explicitly.
jq -s -e 'length == 1 and (.[0] | type == "object") and (.[0].workflow_runs | type == "array")' "$runs_file" >/dev/null 2>&1 \
  || fail "cannot read main CI state (unparsable or empty run list); failing closed"

# Newest completed, non-cancelled push or schedule run of main.
run_id=$(jq -r '[.workflow_runs[]
    | select((.event == "push" or .event == "schedule") and .status == "completed" and .conclusion != "cancelled")]
    | first | .id // empty' "$runs_file" 2>/dev/null)
case "$run_id" in
  ""|*[!0-9]*) fail "no completed main CI run (push or schedule) to judge; failing closed" ;;
esac

if jq -e '[.workflow_runs[] | select((.event == "push" or .event == "schedule") and .status != "completed")] | length > 0' \
  "$runs_file" >/dev/null 2>&1; then
  echo "main-macos-health: a newer main run is in progress and is ignored; judging completed run $run_id"
fi

if [ -z "$jobs_file" ]; then
  # Every job of the judged run, one JSON object per line.
  gh api --paginate "repos/${GITHUB_REPOSITORY}/actions/runs/${run_id}/jobs?per_page=100" --jq '.jobs[]' \
    >"$tmp/jobs.json" 2>"$tmp/err" || fail "cannot read main CI jobs (API error); failing closed"
  jobs_file="$tmp/jobs.json"
fi

jq -s -e 'type == "array" and length > 0 and all(type == "object")' "$jobs_file" >/dev/null 2>&1 \
  || fail "cannot read main CI jobs (unparsable or empty job list); failing closed"

mac_total=$(jq -s '[.[] | select(.name | test("macos"; "i"))] | length' "$jobs_file")
[ "$mac_total" -gt 0 ] || fail "no macOS jobs found in main run $run_id; failing closed"
bad=$(jq -s -r '[.[] | select(.name | test("macos"; "i")) | select(.conclusion != "success" and .conclusion != "skipped")]
    | map("\(.name)=\(.conclusion // "unknown")") | join(", ")' "$jobs_file")
[ -z "$bad" ] || fail "main is red on macOS in run $run_id: $bad. Fix main first, or label the repairing PR $OVERRIDE_LABEL."
echo "main-macos-health: main run $run_id is green on all $mac_total macOS jobs"
