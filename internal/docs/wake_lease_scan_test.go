// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lease protocol classification retains input provenance across control commands.
package docs

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var wakeResolvedLaunchRE = regexp.MustCompile(`^printf '%s\\n' "\$PAYLOAD" \| "\$HARNESS_BIN" (?:-p|exec -) &$`)
var wakeLeaseCommandRE = regexp.MustCompile(`^(?:HOLDER=\$\(|HOLDER="\$\(|OUTCOME=\$\(|OUTCOME="\$\()?mtix inbox lease (?:acquire|release) --agent (?:"\$AGENT"|[A-Za-z_][A-Za-z_0-9-]*)(?: --holder "\$HOLDER")?(?: --ttl (?:15m|1m|24h))?(?: --abandon)?(?:\)"?)?$`)
var wakeLeaseCaptureRE = regexp.MustCompile(`^if PAYLOAD=\$\("\$MTIX_BIN" inbox --agent "\$AGENT" --format prompt\); then STATUS=0; else STATUS=\$\?; fi$`)

func wakeHarnessArgs(harness string) string {
	if harness == "codex" {
		return "exec -"
	}
	return "-p"
}

func wakeIsLeaseScript(text string) bool {
	return strings.HasPrefix(text, "#!/usr/bin/env sh\n") &&
		strings.Contains(text, `"$MTIX_BIN" inbox lease acquire --agent "$AGENT"`) &&
		strings.Contains(text, `"$MTIX_BIN" inbox lease release --agent "$AGENT"`)
}

func wakeLeaseLineAllowed(line string, inputs map[string]bool, protocol bool) bool {
	if wakeResolvedLaunchRE.MatchString(line) || wakeLeaseCaptureRE.MatchString(line) {
		return true
	}
	if wakeReferencesInput(line, inputs) {
		return protocol && line == `if [ -n "$PAYLOAD" ]; then`
	}
	if wakeLeaseCommandRE.MatchString(line) {
		return true
	}
	if !protocol {
		return false
	}
	return wakeProtocolMetadataLine(line)

}

func TestWakeLeaseProtocol_InputConsumers_KeepRefusals(t *testing.T) {
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, form := range []string{
		`printf "$PAYLOAD\n" | "$HARNESS_BIN" -p &`,
		`echo "$PAYLOAD" | "$HARNESS_BIN" -p &`,
		`"$HARNESS_BIN" -p "$PAYLOAD"`,
		`OTHER="$PAYLOAD"; "$HARNESS_BIN" -p`,
		`export PAYLOAD`,
		`report "$PAYLOAD"`,
		`trap 'gemini -p "$MESSAGE"' EXIT`,
		`if true; then gemini -p "$MESSAGE"; fi`,
		`OTHER="$(new-runtime "$MESSAGE")"`,
		`printf '%s\n' "$MESSAGE" | new-runtime`,
		`printf '%s\n' "$PAYLOAD" | xargs "$HARNESS_BIN" -p`,
		`"$MTIX_BIN" inbox lease acquire --agent "$PAYLOAD"`,
	} {
		t.Run(form, func(t *testing.T) { require.NotEmpty(t, wakeLaunchProblems(source+"\n"+form)) })
	}
}
