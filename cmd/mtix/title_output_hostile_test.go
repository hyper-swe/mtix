// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// MTIX-187.7.4: ticket titles and assignees reach the terminal through one
// safe helper, so a title that arrived by sync cannot restyle, overwrite or
// reorder what a reader sees. --json stays the exact record.

// readableTitle is printable non-ASCII text that must stay readable.
const readableTitle = "Caf\u00e9 \u65e5\u672c\u8a9e \U0001F642 ok"

// hostileTitles returns every shared hostile fixture that fits a title (the
// 64 KiB fixtures exceed model.MaxTitleLength and are covered by the format
// tests), plus the readable non-ASCII control case.
func hostileTitles() []testutil.HostileFixture {
	var out []testutil.HostileFixture
	for _, f := range append(testutil.HostileText(), testutil.HostileIDs()...) {
		if len(f.Value) <= model.MaxTitleLength {
			out = append(out, testutil.HostileFixture{
				Name: string(f.Class) + "/" + f.Name, Class: f.Class, Value: f.Value})
		}
	}
	return append(out, testutil.HostileFixture{Name: "readable", Value: readableTitle})
}

// terminalViolation returns a description of the first byte or rune in out
// that must never reach a terminal from stored text, or "".
func terminalViolation(out string) string {
	if !utf8.ValidString(out) {
		return "invalid UTF-8"
	}
	for i := 0; i < len(out); i++ {
		b := out[i]
		if (b < 0x20 && b != '\n') || b == 0x7f {
			return fmt.Sprintf("byte 0x%02x at %d", b, i)
		}
	}
	for i, r := range out {
		if unsafeTerminalRune(r) {
			return fmt.Sprintf("unsafe rune %U at %d", r, i)
		}
	}
	return ""
}

// unsafeTerminalRune reports whether r is a control, line or paragraph
// separator, bidi/zero-width/tag character or any other invisible format
// character, except the directional marks and newline.
func unsafeTerminalRune(r rune) bool {
	switch {
	case r == '\u200e', r == '\u200f', r == '\u061c':
		return false
	case unicode.IsControl(r):
		return r != '\n'
	case r == '\u2028', r == '\u2029':
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// hostileBoard is a seeded board: one parent, one child per fixture, every
// title and assignee hostile.
type hostileBoard struct {
	parent   *model.Node
	children []*model.Node
	titles   []testutil.HostileFixture
}

func seedHostileBoard(t *testing.T) hostileBoard {
	t.Helper()
	initTestApp(t)
	ctx := context.Background()
	parent, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "TEST", Title: "Parent \x1b[31mred\u202e", Creator: "u"})
	require.NoError(t, err)
	b := hostileBoard{parent: parent, titles: hostileTitles()}
	for _, f := range b.titles {
		child, cerr := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "TEST", ParentID: parent.ID, Title: f.Value, Creator: "u"})
		require.NoError(t, cerr, f.Name)
		b.children = append(b.children, child)
	}
	return b
}

// humanCommand is one command writer that prints titles.
type humanCommand struct {
	name string
	run  func(t *testing.T, b hostileBoard)
}

func humanCommands() []humanCommand {
	must := func(t *testing.T, err error) { t.Helper(); require.NoError(t, err) }
	return []humanCommand{
		{"show", func(t *testing.T, b hostileBoard) {
			// Assignees are set directly: hostile ones arrive by sync, which
			// service-level agent-id validation never sees. Done here, not
			// at seeding, because an assigned node is not ready.
			for i, c := range b.children {
				_, err := app.store.WriteDB().ExecContext(context.Background(),
					`UPDATE nodes SET assignee = ? WHERE id = ?`, b.titles[i].Value, c.ID)
				must(t, err)
				must(t, runShow(c.ID))
			}
		}},
		{"list", func(t *testing.T, _ hostileBoard) {
			must(t, runList("", "", "", "", "", "", "", "", 0, false, 1000, "", false))
		}},
		{"ready", func(t *testing.T, _ hostileBoard) { must(t, runReady("", false)) }},
		{"search", func(t *testing.T, _ hostileBoard) {
			must(t, runSearch(nil, "", "", "", "", "", "", "", 1000, "", false))
		}},
		{"tree", func(t *testing.T, b hostileBoard) { must(t, runTree(b.parent.ID, 10)) }},
		{"progress", func(t *testing.T, b hostileBoard) { must(t, runProgress(b.parent.ID)) }},
		{"context", func(t *testing.T, b hostileBoard) {
			for _, c := range b.children {
				must(t, runContext(c.ID, 0, false))
			}
		}},
		{"decompose-dry-run", func(t *testing.T, b hostileBoard) {
			in := make([]service.DecomposeInput, 0, len(b.titles))
			for _, f := range b.titles {
				// Half carry a hostile description too, so both print branches run.
				d := service.DecomposeInput{Title: f.Value}
				if len(in)%2 == 0 {
					d.Description = f.Value
				}
				in = append(in, d)
			}
			must(t, runDecomposeDryRun(b.parent.ID, in))
		}},
		{"create", func(t *testing.T, b hostileBoard) {
			for _, f := range b.titles {
				must(t, runCreate(f.Value, b.parent.ID, "", 3, "", "", "", "", ""))
			}
		}},
		{"agent-work", func(t *testing.T, b hostileBoard) {
			registerTestAgent(t, "hostile-reader")
			for _, c := range b.children {
				_, err := app.store.WriteDB().ExecContext(context.Background(),
					`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`, c.ID, "hostile-reader")
				require.NoError(t, err)
				must(t, runAgentWork("hostile-reader"))
			}
		}},
	}
}

// TestTitleOutput_HostileTitles_NoControlBytes runs every command writer that
// prints a title or assignee over every hostile fixture and asserts the
// output holds no control byte, escape, bidi or zero-width mark.
func TestTitleOutput_HostileTitles_NoControlBytes(t *testing.T) {
	for _, cmd := range humanCommands() {
		t.Run(cmd.name, func(t *testing.T) {
			b := seedHostileBoard(t)
			var errOut string
			out := captureStdout(t, func() {
				errOut = captureStderr(t, func() { cmd.run(t, b) })
			})
			require.NotEmpty(t, out)
			assert.Empty(t, terminalViolation(out), "stdout of %s", cmd.name)
			assert.Empty(t, terminalViolation(errOut), "stderr of %s", cmd.name)
			assert.Contains(t, out, "Caf\u00e9 \u65e5\u672c\u8a9e \U0001F642",
				"printable non-ASCII stays readable")
		})
	}
}

// TestTitleOutput_BypassedHelper_IsDetected documents the bypass mutation: a
// writer that prints a stored title raw is flagged by the same scan, so the
// table test above fails for any command that skips the helper.
func TestTitleOutput_BypassedHelper_IsDetected(t *testing.T) {
	for _, f := range hostileTitles() {
		if f.Name == "readable" {
			assert.Empty(t, terminalViolation(fmt.Sprintf("Title: %s\n", f.Value)))
			continue
		}
		if terminalViolation(fmt.Sprintf("Title: %s\n", f.Value)) == "" {
			continue // printable-only fixture (quotes, slashes, tags): nothing to strip
		}
		assert.NotEmpty(t, terminalViolation(fmt.Sprintf("Title: %s\n", f.Value)), f.Name)
	}
	assert.NotEmpty(t, terminalViolation("Title: \x1b[31mred\n"))
	assert.NotEmpty(t, terminalViolation("Title: a\u202eb\n"))
}

// jsonTitles collects every string under a "title" key anywhere in the JSON
// documents of out (one document per line or one pretty document).
func jsonTitles(t *testing.T, out string) []string {
	t.Helper()
	var titles []string
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var doc any
		require.NoError(t, dec.Decode(&doc))
		collectTitles(doc, &titles)
	}
	return titles
}

func collectTitles(v any, acc *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "title" {
				*acc = append(*acc, s)
				continue
			}
			collectTitles(val, acc)
		}
	case []any:
		for _, e := range x {
			collectTitles(e, acc)
		}
	}
}

// stdlibJSONRoundTrip is what encoding/json makes of a stored title: the
// exact record, with invalid UTF-8 replaced and nothing else touched.
func stdlibJSONRoundTrip(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	var back string
	require.NoError(t, json.Unmarshal(raw, &back))
	return back
}

// TestTitleOutput_JSONPaths_StayByteExact proves --json is not normalized:
// the title in each command's JSON equals the stored title (as encoding/json
// carries it), control and bidi characters included.
func TestTitleOutput_JSONPaths_StayByteExact(t *testing.T) {
	for _, cmd := range humanCommands() {
		if cmd.name == "tree" || cmd.name == "progress" {
			continue // their JSON is the parent record only, titled by the board
		}
		t.Run(cmd.name, func(t *testing.T) {
			b := seedHostileBoard(t)
			app.jsonOutput = true
			var buf bytes.Buffer
			out := captureStdout(t, func() { cmd.run(t, b) })
			buf.WriteString(out)
			got := map[string]bool{}
			for _, s := range jsonTitles(t, buf.String()) {
				got[s] = true
			}
			for _, f := range b.titles {
				assert.Truef(t, got[stdlibJSONRoundTrip(t, f.Value)],
					"%s: JSON lost the exact title of %s", cmd.name, f.Name)
			}
		})
	}
}

// TestNodeRowHelpers_HostileTitle_NoControlBytes covers the row and tree
// helpers directly, so a bypass in either is caught even where no command
// reaches it.
func TestNodeRowHelpers_HostileTitle_NoControlBytes(t *testing.T) {
	for _, f := range hostileTitles() {
		row := strings.Join(FormatNodeRow("TEST-1", "open", 3, f.Value, 0.5, false), " ")
		assert.Empty(t, terminalViolation(row), "FormatNodeRow %s", f.Name)
		line := TreeLine("TEST-1", "open", f.Value, 0.5, "", true, 0, false)
		assert.Empty(t, terminalViolation(line), "TreeLine %s", f.Name)
		child := TreeLine("TEST-1.1", "open", f.Value, 0.5, "  ", false, 1, false)
		assert.Empty(t, terminalViolation(child), "TreeLine child %s", f.Name)
	}
}

// jsonStringsAt collects every string under the given key anywhere in out.
func jsonStringsAt(t *testing.T, out, key string) []string {
	t.Helper()
	var found []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if s, ok := val.(string); ok && k == key {
					found = append(found, s)
					continue
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var doc any
		require.NoError(t, dec.Decode(&doc))
		walk(doc)
	}
	return found
}

// TestTitleOutput_JSONShowAndTree_AssigneeAndParentByteExact proves the show
// --json assignee and the tree --json parent title are the exact stored text.
func TestTitleOutput_JSONShowAndTree_AssigneeAndParentByteExact(t *testing.T) {
	b := seedHostileBoard(t)
	app.jsonOutput = true
	ctx := context.Background()
	for i, c := range b.children {
		_, err := app.store.WriteDB().ExecContext(ctx,
			`UPDATE nodes SET assignee = ? WHERE id = ?`, b.titles[i].Value, c.ID)
		require.NoError(t, err)
		out := captureStdout(t, func() { require.NoError(t, runShow(c.ID)) })
		got := jsonStringsAt(t, out, "assignee")
		require.Len(t, got, 1, b.titles[i].Name)
		assert.Equal(t, stdlibJSONRoundTrip(t, b.titles[i].Value), got[0], b.titles[i].Name)
	}
	out := captureStdout(t, func() { require.NoError(t, runTree(b.parent.ID, 10)) })
	titles := jsonStringsAt(t, out, "title")
	require.NotEmpty(t, titles)
	assert.Equal(t, stdlibJSONRoundTrip(t, b.parent.Title), titles[0])
}
