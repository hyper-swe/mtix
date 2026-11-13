#!/usr/bin/env bash
# remote-bar-env.sh - the HyperSWE environmental-failure marker (MTIX-262).
# Sourced by remote-bar.sh. Lab tooling; nothing here ships to customers.
#
# When the scratch-branch push or the workflow dispatch fails because GitHub
# answered HTTP 5xx or 429, or because the network failed, the wrapper's final
# non-empty output line is exactly
#   hyperswe: environment: <cause>
# (cause at most 200 characters, flush left, only on a non-zero exit) so the
# review loop can tell a host failure from a red bar (HyperSWE docs/loop.md,
# "A bar that reaches a host names its own host failure."). Every other
# failure (auth, ref refusal, tree mismatch, red bar, timeout) prints nothing
# extra. The cause is built here from the stage and the status code, never
# copied from remote output.

RB_ENV_CAUSE_MAX=200
RB_ENV_CAUSE=""

# rb_env_classify <stage> <output>: prints the cause for a push or dispatch
# failure that is HTTP 5xx, HTTP 429 or a network error, and fails (printing
# nothing) otherwise. The first HTTP status in the output decides: any other
# status (401, 403, 404, 422, ...) is not environmental even when the text
# also mentions a connection.
rb_env_classify() {
	local stage="${1:-}" text="${2:-}" lower code
	local http_re='(HTTP/?[0-9.]*[ :]+|returned error: )([0-9]{3})'
	local net_re='connection (reset|refused|timed out|aborted|closed)|operation timed out|i/o timeout|timed out|could not resolve host|no such host|temporary failure in name resolution|network is unreachable|no route to host|unexpected eof|early eof|remote end hung up|error connecting to|failed to connect|tls handshake|broken pipe|rpc failed; curl (18|52|55|56)'
	case "$stage" in push) stage="the scratch-branch push" ;; dispatch) stage="the workflow dispatch" ;; *) return 1 ;; esac
	if [[ "$text" =~ $http_re ]]; then
		code="${BASH_REMATCH[2]}"
		case "$code" in
		5[0-9][0-9] | 429) printf 'GitHub returned HTTP %s on %s\n' "$code" "$stage"; return 0 ;;
		esac
		return 1
	fi
	lower="$(printf '%s' "$text" | tr '[:upper:]' '[:lower:]')"
	if [[ "$lower" =~ $net_re ]]; then
		printf 'network failure reaching GitHub on %s\n' "$stage"
		return 0
	fi
	return 1
}

# rb_env_note <stage> <output>: records the cause when the failure is
# environmental. Called just before rb_die on the push and dispatch paths.
rb_env_note() {
	local cause
	cause="$(rb_env_classify "$1" "$2")" || return 0
	RB_ENV_CAUSE="$cause"
}

# rb_env_marker_line <cause>: prints the marker line for a cause, one line,
# control characters removed, the cause cut to RB_ENV_CAUSE_MAX characters.
rb_env_marker_line() {
	local cause
	cause="$(printf '%s' "$1" | tr -d '\000-\037\177')"
	printf 'hyperswe: environment: %s\n' "${cause:0:$RB_ENV_CAUSE_MAX}"
}

# rb_env_emit <exit-status>: the last thing the wrapper prints. Nothing on exit
# 0 and nothing without a recorded environmental cause.
rb_env_emit() {
	[ "${1:-0}" != 0 ] && [ -n "$RB_ENV_CAUSE" ] || return 0
	rb_env_marker_line "$RB_ENV_CAUSE"
}
