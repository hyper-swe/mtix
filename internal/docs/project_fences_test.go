// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerator_ProjectPointer_FenceExamples(t *testing.T) {
	cases := []struct{ name, open, middle, close string }{
		{"nested-backticks", "````markdown", "```", "````"},
		{"nested-tildes", "~~~~markdown", "~~~", "~~~~"},
		{"mixed-delimiter", "```", "~~~", "```"},
		{"longer-close", "````", "```", "`````"},
		{"closing-text", "```", "```text", "```"},
		{"three-space-indent", "   ```", "    ```", "   ``` \t"},
		{"one-space-indent", " ```", "~~", " ```"},
		{"two-space-indent", "  ~~~", "```", "  ~~~"},
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		for _, tc := range cases {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				pointer := "@MTIX.md"
				if name == "AGENTS.md" {
					pointer = agentsPointer
				}
				input := tc.open + "\n" + tc.middle + "\n" + pointer + "\n" + tc.close + "\n"
				root := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(input), 0600))
				gen, err := NewEmbeddedGenerator(filepath.Join(root, ".mtix", "docs"), minimalTemplateData(), nil)
				require.NoError(t, err)
				gen.SetProjectRoot(root)
				_, err = gen.Generate(false)
				require.NoError(t, err)
				got, err := os.ReadFile(filepath.Join(root, name))
				require.NoError(t, err)
				require.Equal(t, input+pointer+"\n", string(got))
				_, err = gen.Generate(false)
				require.NoError(t, err)
				again, err := os.ReadFile(filepath.Join(root, name))
				require.NoError(t, err)
				require.Equal(t, got, again)
			})
		}
	}
}

func TestGenerator_ProjectPointer_InvalidOpeningAndLivePointer(t *testing.T) {
	for _, input := range []string{
		"```invalid`info\n" + agentsPointer + "\n",
		"    ```\n" + agentsPointer + "\n",
		"``\n" + agentsPointer + "\n",
		"~~~info`allowed\nexample\n~~~\r\n" + agentsPointer,
		agentsPointer + "\n```unclosed\nexample",
	} {
		t.Run(input, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(input), 0600))
			gen, err := NewEmbeddedGenerator(filepath.Join(root, ".mtix", "docs"), minimalTemplateData(), nil)
			require.NoError(t, err)
			gen.SetProjectRoot(root)
			_, err = gen.Generate(false)
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
			require.NoError(t, err)
			require.Equal(t, input, string(got))
		})
	}
}

func TestGenerator_ProjectPointer_UnclosedRefusesBeforeWrites(t *testing.T) {
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		for _, force := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "default", true: "force"}[force], func(t *testing.T) {
				root := t.TempDir()
				_, err := projectGit(root, "init", "-q")
				require.NoError(t, err)
				originals := map[string]string{"MTIX.md": "old briefing", "CLAUDE.md": "claude user", "AGENTS.md": "agents user", ".git/info/exclude": "user exclude"}
				originals[name] = "user\r\n````markdown\r\n```\r\n@MTIX.md"
				for path, content := range originals {
					require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(content), 0600))
				}
				gen, err := NewEmbeddedGenerator(filepath.Join(root, ".mtix", "docs"), minimalTemplateData(), nil)
				require.NoError(t, err)
				gen.SetProjectRoot(root)
				_, err = gen.Generate(force)
				require.ErrorContains(t, err, "unclosed code fence")
				for path, content := range originals {
					got, readErr := os.ReadFile(filepath.Join(root, path))
					require.NoError(t, readErr)
					require.Equal(t, content, string(got))
				}
				require.NoDirExists(t, filepath.Join(root, ".mtix"))
			})
		}
	}
}

func TestAppendProjectPointer_FenceBytesAndHyperSWESuppression(t *testing.T) {
	input := "user\r\n````md\r\n```\r\n" + agentsPointer + "\r\n````"
	got, err := appendProjectPointer([]byte(input), agentsPointer, "AGENTS.md")
	require.NoError(t, err)
	require.Equal(t, input+"\n"+agentsPointer+"\n", string(got))
	root := t.TempDir()
	unclosed := "user\r\n```md\r\nexample"
	require.NoError(t, os.WriteFile(filepath.Join(root, "HYPERSWE.md"), []byte("hyperswe"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(unclosed), 0600))
	gen, err := NewEmbeddedGenerator(filepath.Join(root, ".mtix", "docs"), minimalTemplateData(), nil)
	require.NoError(t, err)
	gen.SetProjectRoot(root)
	_, err = gen.Generate(false)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	require.NoError(t, err)
	require.Equal(t, unclosed, string(data))
	require.NoFileExists(t, filepath.Join(root, "CLAUDE.md"))
}
