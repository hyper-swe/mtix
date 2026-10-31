// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package docs

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWakeGuidance_CaseWordPositions(t *testing.T) {
	script := wakeSource(t, "examples/hooks/wake-agent.sh")
	for _, argument := range []string{"in", "then", "do", "else", "$(true)"} {
		reader := `$M $(case b in a) : ` + argument + ` esac;; b) echo in;; esac)box`
		control := `$M $(case b in a) : ` + argument + ` esac;; b) echo da;; esac)te`
		t.Run(argument, func(t *testing.T) {
			assertWakeTokenPolicy(t, reader, false)
			assertWakeTokenPolicy(t, control, true)
			require.NotEmpty(t, wakeLaunchProblems(script+"\n"+reader))
			require.Empty(t, wakeLaunchProblems(script+"\n"+control))
		})
	}
	for _, pair := range wakeCasePositionPairs() {
		assertWakeTokenPolicy(t, pair[0], false)
		assertWakeTokenPolicy(t, pair[1], true)
		require.NotEmpty(t, wakeLaunchProblems(script+"\n"+pair[0]))
		require.Empty(t, wakeLaunchProblems(script+"\n"+pair[1]))
	}
	for _, word := range []string{
		`$(case a in esac)in`,
		`$(case a in (a) :;; esac)in`,
		`$(case a in a) :;; case) :;; esac)in`,
		`$(case a in a) : in case;; esac)in`,
		`$(case a in case|a) :;; esac)in`,
		`$(case a in a) case b in b) :;; esac;; esac)in`,
	} {
		require.Equal(t, []string{word, "box"}, wakeSpecialWords(word+" box"))
		assertWakeTokenPolicy(t, "echo "+word+" box", true)
	}
}

func wakeCasePositionPairs() [][2]string {
	return [][2]string{
		{`$M $(case esac in a|esac) echo in;; esac)box`, `$M $(case esac in a|esac) echo da;; esac)te`},
		{`$M $(case in in in) echo in;; esac)box`, `$M $(case in in in) echo da;; esac)te`},
		{`$M $(case a in (a) echo in;; esac)box`, `$M $(case a in (a) echo da;; esac)te`},
		{`$M $(case a in a) :;& b) echo in;; esac)box`, `$M $(case a in a) :;& b) echo da;; esac)te`},
		{`$M $(case a in a) :;;& a) echo in;; esac)box`, `$M $(case a in a) :;;& a) echo da;; esac)te`},
		{`$M $(if true; then case a in a) echo in;; esac; fi)box`, `$M $(if true; then case a in a) echo da;; esac; fi)te`},
		{`$M $(for x in a; do case a in a) echo in;; esac; done)box`, `$M $(for x in a; do case a in a) echo da;; esac; done)te`},
	}
}
