#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# cloud-e2e.sh — run the FR-18 sync e2e suite against real cloud providers
# (MTIX-20)
#
# Usage
#   ./scripts/cloud-e2e.sh [supabase|neon|all]     # default: all
#
# Prerequisites
#   .env.test.local at the repo root (gitignored) exporting:
#     MTIX_TEST_SUPABASE_DSN   session-pooler DSN (aws-N-<region>.pooler.
#                              supabase.com:5432, user postgres.<ref>)
#     MTIX_TEST_SUPABASE_CA    path to the Supabase Root 2021 CA cert
#     MTIX_TEST_NEON_DSN       Neon DSN with sslmode=verify-full
#
# Provider quirks this script absorbs (discovered during the
# v0.2.0-beta post-release smoke test, 2026-06-10):
#   * Supabase's direct endpoint is IPv6-only — always use the session
#     pooler (IPv4; session mode supports our advisory locks).
#   * Supabase signs pooler certs with its own CA. The e2e harness's
#     freshHub() calls pgxpool.ParseConfig on the DSN directly,
#     bypassing the MTIX_SYNC_SSLROOTCERT env handling in
#     EnforceTLSPosture — so the sslrootcert must be INLINE in the DSN.
#   * Neon works with verify-full out of the box (public CA), but the
#     dashboard default is sslmode=require; .env.test.local stores the
#     upgraded form.
#
# Safety
#   * The e2e suite DROPS the mtix-owned tables on the target hub at
#     the start of each test. Point this ONLY at throwaway projects.
#   * The DSN is never echoed; output shows provider name + host only.
# ---------------------------------------------------------------------------

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${REPO_ROOT}/.env.test.local"
PROVIDER="${1:-all}"
TIMEOUT="${MTIX_CLOUD_E2E_TIMEOUT:-20m}"

# The full FR-18 edge-case matrix + the FR-20 origin-independent dispatch
# release gate. Update when new TestE2E_* scenarios land.
RUN_PATTERN='TestE2E_Lifecycle|TestE2E_Conflict|TestE2E_Divergent|TestE2E_Repeated|TestE2E_AgentSurge|TestE2E_LostLaptop|TestE2E_QueueFull|TestE2E_Backfill|TestE2E_FR20'

if [ ! -f "${ENV_FILE}" ]; then
  printf 'cloud-e2e: %s not found.\n' "${ENV_FILE}" >&2
  printf 'Create it with MTIX_TEST_SUPABASE_DSN, MTIX_TEST_SUPABASE_CA, MTIX_TEST_NEON_DSN.\n' >&2
  printf 'See the header of this script for the expected formats.\n' >&2
  exit 1
fi

# shellcheck source=/dev/null
source "${ENV_FILE}"

# host_of extracts just the host portion for safe logging (never the
# credentials).
host_of() {
  printf '%s' "$1" | sed -E 's|^[a-z]+://[^@]+@([^/:?]+).*|\1|'
}

run_provider() {
  local name="$1"
  local dsn="$2"

  printf '\n=== %s (%s) ===\n' "${name}" "$(host_of "${dsn}")"
  if MTIX_PG_TEST_DSN="${dsn}" go test -count=1 -timeout "${TIMEOUT}" \
      -run "${RUN_PATTERN}" "${REPO_ROOT}/e2e/..."; then
    RESULTS+=("${name}: PASS")
  else
    RESULTS+=("${name}: FAIL")
    OVERALL_RC=1
  fi
}

build_supabase_dsn() {
  if [ -z "${MTIX_TEST_SUPABASE_DSN:-}" ] || [ -z "${MTIX_TEST_SUPABASE_CA:-}" ]; then
    printf 'cloud-e2e: MTIX_TEST_SUPABASE_DSN / MTIX_TEST_SUPABASE_CA not set in %s\n' "${ENV_FILE}" >&2
    exit 1
  fi
  if [ ! -f "${MTIX_TEST_SUPABASE_CA}" ]; then
    printf 'cloud-e2e: Supabase CA cert not found at %s\n' "${MTIX_TEST_SUPABASE_CA}" >&2
    exit 1
  fi
  # Inline sslmode + sslrootcert because freshHub() bypasses the
  # MTIX_SYNC_SSLROOTCERT env handling (see header).
  local sep='?'
  case "${MTIX_TEST_SUPABASE_DSN}" in
    *\?*) sep='&' ;;
  esac
  printf '%s%ssslmode=verify-full&sslrootcert=%s' \
    "${MTIX_TEST_SUPABASE_DSN}" "${sep}" "${MTIX_TEST_SUPABASE_CA}"
}

RESULTS=()
OVERALL_RC=0

case "${PROVIDER}" in
  supabase)
    run_provider "supabase" "$(build_supabase_dsn)"
    ;;
  neon)
    if [ -z "${MTIX_TEST_NEON_DSN:-}" ]; then
      printf 'cloud-e2e: MTIX_TEST_NEON_DSN not set in %s\n' "${ENV_FILE}" >&2
      exit 1
    fi
    run_provider "neon" "${MTIX_TEST_NEON_DSN}"
    ;;
  all)
    run_provider "neon" "${MTIX_TEST_NEON_DSN}"
    run_provider "supabase" "$(build_supabase_dsn)"
    ;;
  *)
    printf 'cloud-e2e: unknown provider %q (want supabase|neon|all)\n' "${PROVIDER}" >&2
    exit 2
    ;;
esac

printf '\n=== summary ===\n'
for r in "${RESULTS[@]}"; do
  printf '  %s\n' "${r}"
done

exit "${OVERALL_RC}"
