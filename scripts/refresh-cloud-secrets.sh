#!/usr/bin/env bash
#
# Wake the cloud-contract gate's databases and refresh its GitHub secrets
# (MTIX-42). Both providers idle-suspend, which silently rots the release gate:
# v0.5.1-beta failed with Neon "connection refused" and Supabase "tenant not
# found" five weeks after the last green run.
#
# Provider management APIs do the work, so this is re-runnable whenever the
# gate rots again rather than a one-off paste of hand-copied URLs.
#
# Usage — no secret is stored in this file, so it is safe to commit:
#
#   bash scripts/refresh-cloud-secrets.sh
#
# It reads NEON_API_KEY and SUPABASE_ACCESS_TOKEN from .env.cloud (gitignored),
# or from the environment if you would rather export them.
#   Neon:     console.neon.tech -> user menu -> Account settings -> API keys
#   Supabase: supabase.com/dashboard/account/tokens
#
# The Supabase database password is read from .env.cloud (gitignored) by
# matching the DSN whose user is postgres.$SUPABASE_REF. Override with
# SUPABASE_DB_PASSWORD if you would rather pass it explicitly.
#
# Why it must come from somewhere: the Management API can restore a project and
# report its pooler config, but NO endpoint returns an existing database
# password — only POST /v1/projects/{ref}/database/password, which sets a new
# one. It is the single value the API cannot hand back.
#
# NOTE the Supabase password contains an unencoded '@'. Anything parsing these
# DSNs must split on the LAST '@', not the first — the same hazard MTIX-61 hit
# when libpq mis-split a password like this. The loader below does that.
#
# Every DSN is proved to open a REAL connection before its secret is written.
# That is deliberate: the CI canary reports PASS whenever the secret merely
# exists (MTIX-86) — which is how a green tick preceded every test failing to
# connect. Do not weaken this to a presence check.

set -euo pipefail

NEON_ORG_ID="${NEON_ORG_ID:-}"                   # optional; auto-detected below
NEON_PROJECT_ID="${NEON_PROJECT_ID:-}"           # optional; auto-detected below
NEON_DB="${NEON_DB:-neondb}"
NEON_ROLE="${NEON_ROLE:-neondb_owner}"
SUPABASE_REF="${SUPABASE_REF:-srpezofnqckkiwiizhti}"
SUPABASE_CA_FILE="${SUPABASE_CA_FILE:-}"         # PEM; Supabase chains to a private CA

NEON_API="https://console.neon.tech/api/v2"
SUPABASE_API="https://api.supabase.com/v1"

cd "$(git rev-parse --show-toplevel)"

die() { echo "FAIL: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null || die "$1 not found"; }

have gh; have jq; have curl; have go
gh auth status >/dev/null 2>&1 || die "gh not authenticated — run 'gh auth login'"

ENV_CLOUD="${ENV_CLOUD:-.env.cloud}"

# Take the provider API tokens from .env.cloud unless already exported.
# Plain KEY=VALUE only — the DSN password needs different handling (below),
# because it may contain an unencoded '@'.
load_from_env_cloud() {
  local key="$1" line val
  [ -r "$ENV_CLOUD" ] || return 1
  line="$(grep -m1 -E "^[[:space:]]*(export[[:space:]]+)?${key}=" "$ENV_CLOUD" || true)"
  [ -n "$line" ] || return 1
  val="${line#*=}"
  val="${val%\"}"; val="${val#\"}"; val="${val%\'}"; val="${val#\'}"
  printf '%s' "$val"
}

: "${NEON_API_KEY:=$(load_from_env_cloud NEON_API_KEY || true)}"
: "${SUPABASE_ACCESS_TOKEN:=$(load_from_env_cloud SUPABASE_ACCESS_TOKEN || true)}"
[ -n "${NEON_API_KEY:-}" ] || die "NEON_API_KEY not set and not found in $ENV_CLOUD"
[ -n "${SUPABASE_ACCESS_TOKEN:-}" ] || die "SUPABASE_ACCESS_TOKEN not set and not found in $ENV_CLOUD"
echo "==> credentials: NEON_API_KEY (${#NEON_API_KEY} chars), SUPABASE_ACCESS_TOKEN (${#SUPABASE_ACCESS_TOKEN} chars)"
# Pull the password for this ref out of .env.cloud unless one was passed in.
# Splits on the LAST '@' so an unencoded '@' inside the password survives.
if [ -z "${SUPABASE_DB_PASSWORD:-}" ]; then
  [ -r "$ENV_CLOUD" ] || die "no SUPABASE_DB_PASSWORD and cannot read $ENV_CLOUD"
  while IFS= read -r line; do
    case "$line" in ''|\#*) continue ;; esac
    val="${line#*=}"; val="${val%\"}"; val="${val#\"}"; val="${val%\'}"; val="${val#\'}"
    case "$val" in *://*) ;; *) continue ;; esac
    rest="${val#*://}"
    userinfo="${rest%@*}"                       # everything before the LAST '@'
    u="${userinfo%%:*}"
    if [ "$u" = "postgres.$SUPABASE_REF" ]; then
      SUPABASE_DB_PASSWORD="${userinfo#*:}"
      echo "    password: taken from $ENV_CLOUD (user $u, ${#SUPABASE_DB_PASSWORD} chars)"
      break
    fi
  done < "$ENV_CLOUD"
fi
[ -n "${SUPABASE_DB_PASSWORD:-}" ] || \
  die "no DSN in $ENV_CLOUD for user postgres.$SUPABASE_REF — set SUPABASE_DB_PASSWORD, or SUPABASE_REF to a ref present there"

# --fail-with-body so an API error prints its message rather than just an exit
# code (a bare "curl: (56) ... error: 400" cost a debugging round-trip).
_api() {
  local tok="$1"; shift
  local out code
  out="$(curl -sS -w '\n%{http_code}' -H "Authorization: Bearer $tok" "$@")" || return 1
  code="${out##*$'\n'}"; out="${out%$'\n'*}"
  case "$code" in
    2*) printf '%s' "$out" ;;
    *)  echo "HTTP $code from ${*: -1}" >&2; echo "$out" >&2; return 1 ;;
  esac
}
neon() { _api "$NEON_API_KEY" "$@"; }
supa() { _api "$SUPABASE_ACCESS_TOKEN" "$@"; }

# Fail loudly with the raw payload when a field is missing, rather than
# building a silently broken DSN out of empty strings.
pluck() {
  local json="$1" filter="$2" what="$3" out
  out="$(jq -r "$filter // empty" <<<"$json")"
  [ -n "$out" ] || { echo "--- unexpected payload ---" >&2; jq . <<<"$json" >&2; die "could not read $what"; }
  printf '%s' "$out"
}

# Non-destructive probe: pgx is the same driver, TLS and channel-binding path
# the product uses, so verify-full and a private CA get exercised for real.
# Runs SELECT 1 only — it never touches mtix tables. (The transport tests call
# freshSchema, which DROPS them. Deliberately not used here.)
PROBE_DIR="$(mktemp -d)"; trap 'rm -rf "$PROBE_DIR"' EXIT
cat > "$PROBE_DIR/main.go" <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("PROBE_DSN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)
	var one int
	if err := conn.QueryRow(ctx, "select 1").Scan(&one); err != nil {
		fmt.Fprintln(os.Stderr, "query:", err)
		os.Exit(1)
	}
	var ver string
	_ = conn.QueryRow(ctx, "select version()").Scan(&ver)
	if len(ver) > 64 {
		ver = ver[:64]
	}
	fmt.Println("    connected:", ver)
}
GO

probe() { # probe <dsn> [ca_file]
  if [ -n "${2:-}" ]; then PGSSLROOTCERT="$2" PROBE_DSN="$1" go run "$PROBE_DIR/main.go"
  else PROBE_DSN="$1" go run "$PROBE_DIR/main.go"; fi
}

# Force verify-full: provider dashboards hand out sslmode=require, which
# encrypts but does NOT verify the server certificate.
harden() {
  local dsn="$1" base query
  case "$dsn" in
    *\?*) base="${dsn%%\?*}"; query="${dsn#*\?}" ;;
    *)    base="$dsn";        query="" ;;
  esac
  query="$(sed -E 's/(^|&)sslmode=[^&]*//g; s/&&+/\&/g; s/^&+//; s/&+$//' <<<"$query")"
  if [ -n "$query" ]; then printf '%s?%s&sslmode=verify-full' "$base" "$query"
  else printf '%s?sslmode=verify-full' "$base"; fi
}

# ─── Neon ────────────────────────────────────────────────────────────────────
echo "==> Neon"
if [ -z "$NEON_PROJECT_ID" ]; then
  # GET /projects requires org_id ("org_id is required, you can find it on your
  # organization settings page") — discover it rather than hardcoding.
  if [ -z "${NEON_ORG_ID:-}" ]; then
    orgs="$(neon "$NEON_API/users/me/organizations")"
    NEON_ORG_ID="$(pluck "$orgs" '.organizations[0].id' 'neon org id')"
    echo "    org: $NEON_ORG_ID ($(jq -r '.organizations[0].name // "?"' <<<"$orgs"))"
    [ "$(jq '.organizations | length' <<<"$orgs")" = "1" ] || \
      echo "    NOTE: more than one org; set NEON_ORG_ID to pin it"
  fi
  projects="$(neon --get --data-urlencode "org_id=$NEON_ORG_ID" "$NEON_API/projects")"
  NEON_PROJECT_ID="$(pluck "$projects" '.projects[0].id' 'neon project id')"
  echo "    project: $NEON_PROJECT_ID ($(jq -r '.projects[0].name // "?"' <<<"$projects"))"
  [ "$(jq '.projects | length' <<<"$projects")" = "1" ] || \
    echo "    NOTE: more than one project; set NEON_PROJECT_ID to pin it"
fi

endpoints="$(neon "$NEON_API/projects/$NEON_PROJECT_ID/endpoints")"
ep_id="$(pluck "$endpoints" '.endpoints[0].id' 'neon endpoint id')"
ep_state="$(jq -r '.endpoints[0].current_state // "unknown"' <<<"$endpoints")"
echo "    endpoint: $ep_id (state: $ep_state)"
if [ "$ep_state" != "active" ]; then
  echo "    starting suspended endpoint..."
  neon -X POST "$NEON_API/projects/$NEON_PROJECT_ID/endpoints/$ep_id/start" >/dev/null || true
  for _ in $(seq 1 30); do
    sleep 4
    ep_state="$(neon "$NEON_API/projects/$NEON_PROJECT_ID/endpoints/$ep_id" | jq -r '.endpoint.current_state // "unknown"')"
    [ "$ep_state" = "active" ] && break
  done
  echo "    endpoint state: $ep_state"
fi

# The flat /projects/{id}/connection_uri is the live route; the nested
# /branches/{bid}/databases/{db}/connection_uri form in some docs 404s.
# pooled=true returns the -pooler host, which is what the suite targets.
neon_uri="$(pluck "$(neon --get \
  --data-urlencode "database_name=$NEON_DB" \
  --data-urlencode "role_name=$NEON_ROLE" \
  --data-urlencode "pooled=true" \
  "$NEON_API/projects/$NEON_PROJECT_ID/connection_uri")" \
  '.uri' 'neon connection uri')"

case "$neon_uri" in
  *:*@*) ;;
  *) die "connection_uri came back without credentials; set the DSN manually" ;;
esac

NEON_DSN="$(harden "$neon_uri")"
echo "    probing (verify-full)..."
probe "$NEON_DSN"

# ─── Supabase ────────────────────────────────────────────────────────────────
echo "==> Supabase"
status="$(supa "$SUPABASE_API/projects" | jq -r --arg r "$SUPABASE_REF" '.[] | select(.id==$r) | .status // "UNKNOWN"')"
[ -n "$status" ] || die "project $SUPABASE_REF not visible to this token"
echo "    project $SUPABASE_REF status: $status"

if [ "$status" != "ACTIVE_HEALTHY" ]; then
  echo "    restoring paused project..."
  supa -X POST -H 'Content-Type: application/json' -d '{}' \
    "$SUPABASE_API/projects/$SUPABASE_REF/restore" >/dev/null || \
    die "restore call failed — restore it from the dashboard, then re-run"
  for _ in $(seq 1 60); do
    sleep 10
    status="$(supa "$SUPABASE_API/projects" | jq -r --arg r "$SUPABASE_REF" '.[] | select(.id==$r) | .status // "UNKNOWN"')"
    echo "    ... $status"
    [ "$status" = "ACTIVE_HEALTHY" ] && break
  done
  [ "$status" = "ACTIVE_HEALTHY" ] || die "project did not reach ACTIVE_HEALTHY (last: $status)"
fi

# The live route is config/database/pooler; database/pooler-config 404s.
# It returns an ARRAY, and db_port is the TRANSACTION pooler (6543) — the
# gate wants the SESSION pooler on 5432, set explicitly below.
pooler="$(supa "$SUPABASE_API/projects/$SUPABASE_REF/config/database/pooler")"
p_host="$(pluck "$pooler" '(.[0].db_host // .db_host)' 'pooler host')"
p_user="$(pluck "$pooler" '(.[0].db_user // .db_user)' 'pooler user')"
p_name="$(pluck "$pooler" '(.[0].db_name // .db_name)' 'pooler database')"
# The gate wants the SESSION pooler on 5432, not the transaction pooler (6543).
SUPABASE_DSN="postgresql://${p_user}:${SUPABASE_DB_PASSWORD}@${p_host}:5432/${p_name}?sslmode=verify-full"
echo "    pooler: ${p_host}:5432 user=${p_user} db=${p_name}"

if [ -z "$SUPABASE_CA_FILE" ]; then
  SUPABASE_CA_FILE="$PROBE_DIR/supabase-ca.crt"
  echo "    fetching CA chain..."
  # The bucket lives in ap-southeast-1; the eu-west-1 hostname 301s to an XML
  # error body that is not a certificate. -L follows, and the PEM check below
  # catches anything that is not one.
  curl -fsSL -o "$SUPABASE_CA_FILE" \
    "https://supabase-downloads.s3-ap-southeast-1.amazonaws.com/prod/ssl/prod-ca-2021.crt" || \
    die "could not fetch the CA; download it from the dashboard and set SUPABASE_CA_FILE"
fi
grep -q "BEGIN CERTIFICATE" "$SUPABASE_CA_FILE" || die "$SUPABASE_CA_FILE is not a PEM certificate"

echo "    probing (verify-full against the private CA)..."
probe "$SUPABASE_DSN" "$SUPABASE_CA_FILE"

# ─── Write secrets ───────────────────────────────────────────────────────────
# Values go over stdin, never argv, so they never appear in the process table.
echo "==> setting repo secrets"
printf '%s' "$NEON_DSN"     | gh secret set MTIX_TEST_NEON_DSN
printf '%s' "$SUPABASE_DSN" | gh secret set MTIX_TEST_SUPABASE_DSN
gh secret set MTIX_TEST_SUPABASE_CA < "$SUPABASE_CA_FILE"

echo
gh secret list | grep MTIX_TEST

cat <<'NEXT'

Both databases are awake and both DSNs proved a real connection.
Re-run the release against the existing tag — the tree is unchanged, so
no re-tag is needed:

  gh run rerun 33542273579 --failed
NEXT
