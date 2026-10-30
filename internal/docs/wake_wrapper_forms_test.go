// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.26 checks transparent wrappers before classifying the effective head.
func TestWakeGuidance_WrappedUnknownReaders_Checked(t *testing.T) {
	wrappers := []string{
		"env %s", "env NOTE=value %s", "env NOTE='two words' %s", `env NOTE=two\ words %s`,
		"env -i -u OTHER -C board NOTE=value %s", "env --unset OTHER --chdir board %s",
		"env --unset=OTHER --chdir=board -- %s", "env -S '%s'", "env --split-string='%s'",
		"/usr/bin/env %s", "/usr/bin/env --ignore-environment %s", "timeout 5 %s", "timeout -s TERM -k 2 5 %s",
		"timeout --signal=TERM --kill-after=2 --foreground 5s %s", "nice %s", "nice -n 5 %s",
		"nice --adjustment 5 %s", "nice -5 %s", "nohup %s", "nohup -- %s",
		"exec %s", "exec -a 'worker name' %s", "exec -cl %s", "command %s", "command -p -- %s",
		"time -p %s", "time -f 'elapsed time' -o result %s", "sh -c '%s'", "bash -c '%s'",
		"bash --noprofile -lc '%s'", "bash --rcfile 'config file' -c '%s'",
		"sh -c 'date -u; %s'", "env X=1 timeout 5 nice -n 1 nohup %s",
		"sh -c 'env -u OTHER %s'", "env -S 'timeout 5 %s'", "env -- %s",
		`env NOTE="a\" b" %s`, "env --split-string '%s'", "env --argv0 'worker name' %s",
		"timeout --signal TERM --kill-after 2 5s %s", "nice --adjustment=5 %s",
		"time --format 'elapsed time' --output result %s",
	}
	for _, wrapper := range wrappers {
		t.Run(wrapper, func(t *testing.T) {
			for _, command := range []string{`$M inbox --format prompt`, `fetch -C board inbox`, `mtix inbox --format prompt`, `fetch --project-dir="board path" inbox`, `fetch in\box`} {
				text := fmt.Sprintf(wrapper, command)
				if wrapper == "command %s" && command == "mtix inbox --format prompt" {
					require.Empty(t, wakeLaunchProblems(text))
					require.Empty(t, wakeLaunchProblems("Use `"+text+"`.", true))
					text += " | next-runtime"
				}
				for _, document := range []bool{false, true} {
					assertWakeWrappedSource(t, text, document)
				}
			}
			for _, command := range []string{`date -u`, `$M --help`, `$TOOL run`} {
				assertWakeWrappedUnrelated(t, fmt.Sprintf(wrapper, command))
			}
		})
	}
}

func assertWakeWrappedUnrelated(t *testing.T, source string) {
	t.Helper()
	for _, text := range []string{source, `FRESH="$(` + source + `)"` + "; " + `next-runtime "$FRESH"`,
		`next-runtime "$(` + source + `)"`, `read FRESH < <(` + source + `)`,
	} {
		require.Empty(t, wakeLaunchProblems(text), "%s", text)
		require.Empty(t, wakeLaunchProblems("Use `"+text+"`.", true), "%s", text)
		require.Empty(t, wakeLaunchProblems("```sh\n"+text+"\n```", true), "%s", text)
	}
}

func assertWakeWrappedSource(t *testing.T, source string, document bool) {
	t.Helper()
	for _, text := range []string{source, `FRESH="$(` + source + `)"` + "; " + `next-runtime "$FRESH"`,
		`next-runtime "$(` + source + `)"`, `read FRESH < <(` + source + `)`,
	} {
		if document {
			require.NotEmpty(t, wakeLaunchProblems("Use `"+text+"`.", true), "%s", text)
			text = "```sh\n" + text + "\n```"
		}
		require.NotEmpty(t, wakeLaunchProblems(text, document), "%s", text)
	}
}

func TestWakeGuidance_UnknownHeadPolicy_Conservative(t *testing.T) {
	require.NotEmpty(t, wakeLaunchProblems("echo inbox"))
	require.NotEmpty(t, wakeLaunchProblems("Use `echo inbox`.", true))
	require.Empty(t, wakeLaunchProblems("An example such as echo inbox is ordinary prose here.", true))
	require.Empty(t, wakeLaunchProblems("Use `$M --help` or `$TOOL run`.", true))
	for _, text := range []string{"command -v fetch", "command -V fetch", "env", "sh --help", "bash script input"} {
		require.Empty(t, wakeLaunchProblems(text), "%s", text)
		require.Empty(t, wakeLaunchProblems("Use `"+text+"`.", true), "%s", text)
	}
}
