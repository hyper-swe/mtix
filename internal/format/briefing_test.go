// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func makeBriefingNode(id, title, desc, prompt, acceptance string) *model.Node {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	return &model.Node{
		ID:          id,
		Project:     "PROJ",
		Depth:       0,
		Seq:         1,
		Title:       title,
		Description: desc,
		Prompt:      prompt,
		Acceptance:  acceptance,
		NodeType:    model.NodeTypeEpic,
		Priority:    model.PriorityCritical,
		Status:      model.StatusOpen,
		Assignee:    "agent-a",
		Weight:      1.0,
		ContentHash: "abc",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TestRenderBriefing_BasicFormat verifies the core briefing output format
// per FR-17.5: delimited blocks, labeled fields, multi-line handling.
func TestRenderBriefing_BasicFormat(t *testing.T) {
	nodes := []*model.Node{
		makeBriefingNode("PROJ-1", "Build auth", "OAuth2 flow", "Implement login", "Tests pass"),
	}

	var buf bytes.Buffer
	err := RenderBriefing(&buf, nodes, BriefingOpts{})
	require.NoError(t, err)
	out := buf.String()

	// Must contain 80-char separator.
	assert.Contains(t, out, strings.Repeat("=", 80))
	// Must contain field labels.
	assert.Contains(t, out, "ID: PROJ-1")
	assert.Contains(t, out, "TITLE: Build auth")
	assert.Contains(t, out, "STATUS: open")
	assert.Contains(t, out, "PRIORITY: 1")
	assert.Contains(t, out, "NODE_TYPE: epic")
	assert.Contains(t, out, "ASSIGNEE: agent-a")
	// Multi-line fields should use indented block.
	assert.Contains(t, out, "DESCRIPTION:")
	assert.Contains(t, out, "  OAuth2 flow")
	assert.Contains(t, out, "PROMPT:")
	assert.Contains(t, out, "  Implement login")
	assert.Contains(t, out, "ACCEPTANCE:")
	assert.Contains(t, out, "  Tests pass")
}

// TestRenderBriefing_MultipleNodes verifies separator between nodes.
func TestRenderBriefing_MultipleNodes(t *testing.T) {
	nodes := []*model.Node{
		makeBriefingNode("PROJ-1", "First", "", "prompt1", ""),
		makeBriefingNode("PROJ-2", "Second", "", "prompt2", ""),
	}

	var buf bytes.Buffer
	err := RenderBriefing(&buf, nodes, BriefingOpts{})
	require.NoError(t, err)
	out := buf.String()

	// Two separators (one before each node).
	sepCount := strings.Count(out, strings.Repeat("=", 80))
	assert.Equal(t, 2, sepCount, "should have one separator per node")

	assert.Contains(t, out, "ID: PROJ-1")
	assert.Contains(t, out, "ID: PROJ-2")
}

// TestRenderBriefing_EmptyFieldsOmitted verifies that empty/zero fields
// are omitted by default per FR-17.5.
func TestRenderBriefing_EmptyFieldsOmitted(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "", "", "")
	node.Description = ""
	node.Prompt = ""
	node.Acceptance = ""
	node.Assignee = ""

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{})
	require.NoError(t, err)
	out := buf.String()

	assert.NotContains(t, out, "DESCRIPTION:")
	assert.NotContains(t, out, "PROMPT:")
	assert.NotContains(t, out, "ACCEPTANCE:")
	assert.NotContains(t, out, "ASSIGNEE:")
}

// TestRenderBriefing_ShowEmpty verifies that --show-empty includes
// empty fields per FR-17.5.
func TestRenderBriefing_ShowEmpty(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "", "", "")
	node.Description = ""
	node.Assignee = ""

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{ShowEmpty: true})
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "DESCRIPTION:")
	assert.Contains(t, out, "ASSIGNEE:")
}

// TestRenderBriefing_MaxFieldChars verifies field truncation with
// explicit marker per FR-17.4.
func TestRenderBriefing_MaxFieldChars(t *testing.T) {
	longPrompt := strings.Repeat("x", 200)
	node := makeBriefingNode("PROJ-1", "Title", "", longPrompt, "")

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{MaxFieldChars: 50})
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "...[truncated]")
	// The prompt should be cut before 200 chars.
	assert.Less(t, len(out), 500, "output should be shorter than untruncated")
}

// TestRenderBriefing_ControlCharsSanitized verifies that control
// characters are replaced with U+FFFD per FR-17.5 / FR-17 audit T10.
func TestRenderBriefing_ControlCharsSanitized(t *testing.T) {
	// Embed ANSI escape sequence and null byte in title.
	maliciousTitle := "Normal\x1b[2J\x1b[HEvil\x00End"
	node := makeBriefingNode("PROJ-1", maliciousTitle, "", "", "")

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{})
	require.NoError(t, err)
	out := buf.String()

	// ANSI escape \x1b should be replaced with U+FFFD.
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x00")
	assert.Contains(t, out, "\uFFFD")
	// Non-control parts preserved.
	assert.Contains(t, out, "Normal")
	assert.Contains(t, out, "Evil")
	assert.Contains(t, out, "End")
}

// TestRenderBriefing_NewlineInTitle_PromotedToMultiline verifies that
// a single-line field containing newlines is auto-promoted to the
// multi-line block format per FR-17.5 / FR-17 audit T11.
func TestRenderBriefing_NewlineInTitle_PromotedToMultiline(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Line1\nLine2\nLine3", "", "", "")

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{})
	require.NoError(t, err)
	out := buf.String()

	// Should be promoted to multi-line: "TITLE:\n  Line1\n  Line2\n  Line3"
	// NOT "TITLE: Line1\nLine2\nLine3" which would allow label injection.
	assert.Contains(t, out, "TITLE:\n")
	assert.Contains(t, out, "  Line1\n")
	assert.Contains(t, out, "  Line2\n")
	assert.Contains(t, out, "  Line3\n")
}

// TestRenderBriefing_TabsPreserved verifies that tabs are preserved
// in multi-line content per FR-17.5.
func TestRenderBriefing_TabsPreserved(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "", "Step 1:\n\tdo A\n\tdo B", "")

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{})
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "\tdo A")
	assert.Contains(t, out, "\tdo B")
}

// TestRenderBriefing_EmptyNodes verifies that empty node list produces
// no output.
func TestRenderBriefing_EmptyNodes(t *testing.T) {
	var buf bytes.Buffer
	err := RenderBriefing(&buf, nil, BriefingOpts{})
	require.NoError(t, err)
	assert.Empty(t, buf.String())
}

// TestRenderBriefing_WithFields verifies that --fields restricts
// which fields appear in the briefing output.
func TestRenderBriefing_WithFields(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "Desc", "Prompt", "Accept")

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{
		Fields: []string{"id", "title", "prompt"},
	})
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "ID: PROJ-1")
	assert.Contains(t, out, "TITLE: Title")
	assert.Contains(t, out, "PROMPT:")
	// Excluded fields should NOT appear.
	assert.NotContains(t, out, "DESCRIPTION:")
	assert.NotContains(t, out, "ACCEPTANCE:")
	assert.NotContains(t, out, "STATUS:")
}

// TestRenderBriefing_InvalidFields verifies that invalid field names
// return ErrInvalidInput.
func TestRenderBriefing_InvalidFields(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "", "", "")

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{
		Fields: []string{"id", "nonexistent"},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestRenderBriefing_GenericFieldOutsideDefaults verifies that requesting
// a field not in the default briefing set (e.g., "depth", "seq") still
// works via the generic projection fallback.
func TestRenderBriefing_GenericFieldOutsideDefaults(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "", "", "")
	node.Depth = 3
	node.Seq = 7

	var buf bytes.Buffer
	err := RenderBriefing(&buf, []*model.Node{node}, BriefingOpts{
		Fields: []string{"id", "depth", "seq"},
	})
	require.NoError(t, err)
	out := buf.String()

	assert.Contains(t, out, "ID: PROJ-1")
	assert.Contains(t, out, "DEPTH: 3")
	assert.Contains(t, out, "SEQ: 7")
}

// TestRenderBriefing_WriterError verifies that io.Writer errors propagate.
func TestRenderBriefing_WriterError(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "", "", "")
	err := RenderBriefing(&failWriter{}, []*model.Node{node}, BriefingOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}

// failWriter is an io.Writer that always returns an error.
type failWriter struct{}

func (f *failWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("write failed")
}

// TestRenderBriefing_Deterministic verifies that two calls with the
// same input produce byte-identical output per FR-17.4.
func TestRenderBriefing_Deterministic(t *testing.T) {
	node := makeBriefingNode("PROJ-1", "Title", "Desc", "Prompt", "Accept")

	var buf1, buf2 bytes.Buffer
	require.NoError(t, RenderBriefing(&buf1, []*model.Node{node}, BriefingOpts{}))
	require.NoError(t, RenderBriefing(&buf2, []*model.Node{node}, BriefingOpts{}))

	assert.Equal(t, buf1.String(), buf2.String(), "output must be deterministic")
}
