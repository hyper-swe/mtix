// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

// MTIX-225 (research MTIX-224): the default context --json and mtix_context
// payload carries each ancestor's prompt once, in assembled_prompt. The full
// structured chain is opt-in (--chain / chain:true).

// ctxFiller makes each prompt a realistic size (about 400 characters), so the
// fixed per-entry metadata of the slim chain is measured against real prompts.
var ctxFiller = strings.Repeat(" The agent must read the constraints, keep the change small and cover every error path.", 5)

var (
	ctxPromptRoot  = "ROOT-PROMPT-ALPHA-7731 build the whole subsystem carefully." + ctxFiller
	ctxPromptEpic  = "EPIC-PROMPT-BRAVO-7732 implement the storage module." + ctxFiller
	ctxPromptIssue = "ISSUE-PROMPT-CHARLIE-7733 write the parser function." + ctxFiller
	ctxPromptLeaf  = "LEAF-PROMPT-DELTA-7734 add the failing test first." + ctxFiller
)

const ctxAnnotation = "ANNOT-ECHO-7735 clarify the rollback rule"

// seedDepth3Context creates a four-node chain (depth 0..3) with unique prompts
// and one unresolved annotation on the root, and returns the leaf id.
func seedDepth3Context(t *testing.T) string {
	t.Helper()
	initTestApp(t)
	require.NoError(t, runCreate("Root", "", "", 3, "Root description", ctxPromptRoot, "", "", ""))
	require.NoError(t, runCreate("Epic", "TEST-1", "", 3, "Epic description", ctxPromptEpic, "", "", ""))
	require.NoError(t, runCreate("Issue", "TEST-1.1", "", 3, "Issue description", ctxPromptIssue, "", "", ""))
	require.NoError(t, runCreate("Leaf", "TEST-1.1.1", "", 3, "Leaf description", ctxPromptLeaf, "", "", ""))
	require.NoError(t, runAnnotate("TEST-1", ctxAnnotation))
	return "TEST-1.1.1.1"
}

type ctxPayload struct {
	Chain []struct {
		ID          string          `json:"id"`
		Title       string          `json:"title"`
		Status      string          `json:"status"`
		Prompt      string          `json:"prompt"`
		Description string          `json:"description"`
		Annotations json.RawMessage `json:"annotations"`
	} `json:"chain"`
	AssembledPrompt string `json:"assembled_prompt"`
}

func decodeCtxPayload(t *testing.T, raw string) ctxPayload {
	t.Helper()
	var p ctxPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	return p
}

func ctxJSONOutput(t *testing.T, id string, chain bool) string {
	t.Helper()
	app.jsonOutput = true
	return captureStdout(t, func() { require.NoError(t, runContext(id, 0, chain)) })
}

func assertAncestorPromptsOnce(t *testing.T, payload string, want int) {
	t.Helper()
	for _, p := range []string{ctxPromptRoot, ctxPromptEpic, ctxPromptIssue, ctxPromptLeaf} {
		assert.Equal(t, want, strings.Count(payload, p), "prompt %q occurrences", p)
	}
}

func TestContextJSON_DefaultDepth3_EachAncestorPromptOnce(t *testing.T) {
	id := seedDepth3Context(t)
	out := ctxJSONOutput(t, id, false)
	assertAncestorPromptsOnce(t, out, 1)

	p := decodeCtxPayload(t, out)
	require.Len(t, p.Chain, 4, "chain keeps one entry per node")
	for i, e := range p.Chain {
		assert.NotEmpty(t, e.ID)
		assert.NotEmpty(t, e.Title)
		assert.NotEmpty(t, e.Status)
		assert.Empty(t, e.Prompt, "entry %d prompt", i)
		assert.Empty(t, e.Description, "entry %d description", i)
		assert.Empty(t, e.Annotations, "entry %d annotations", i)
	}
}

func TestContextJSON_DefaultDepth3_SizeWithin1Point3OfAssembledPrompt(t *testing.T) {
	id := seedDepth3Context(t)
	out := ctxJSONOutput(t, id, false)
	p := decodeCtxPayload(t, out)
	require.NotEmpty(t, p.AssembledPrompt)
	assert.LessOrEqual(t, float64(len(out)), 1.3*float64(len(p.AssembledPrompt)),
		"payload %d bytes vs assembled_prompt %d bytes", len(out), len(p.AssembledPrompt))
}

func TestContextJSON_Default_AncestorAnnotationReachesAssembledPrompt(t *testing.T) {
	id := seedDepth3Context(t)
	p := decodeCtxPayload(t, ctxJSONOutput(t, id, false))
	assert.Contains(t, p.AssembledPrompt, "[ANNOTATION by ")
	assert.Contains(t, p.AssembledPrompt, ctxAnnotation)
}

func TestContextJSON_ChainFlag_RestoresFullStructuredChain(t *testing.T) {
	id := seedDepth3Context(t)
	out := ctxJSONOutput(t, id, true)
	assertAncestorPromptsOnce(t, out, 2)

	p := decodeCtxPayload(t, out)
	require.Len(t, p.Chain, 4)
	assert.Equal(t, ctxPromptRoot, p.Chain[0].Prompt)
	assert.Equal(t, "Epic description", p.Chain[1].Description)
	assert.NotEmpty(t, p.Chain[0].Annotations)
	assert.Contains(t, p.AssembledPrompt, ctxAnnotation)
}

func TestContextCmd_ChainFlag_IsRegistered(t *testing.T) {
	f := newContextCmd().Flags().Lookup("chain")
	require.NotNil(t, f)
	assert.Equal(t, "false", f.DefValue)
}

func callMCPContext(t *testing.T, args string) string {
	t.Helper()
	reg := mcp.NewToolRegistry()
	mcp.RegisterContextTools(reg, app.ctxSvc, app.promptSvc)
	res, err := reg.Call(context.Background(), "mtix_context", json.RawMessage(args))
	require.NoError(t, err)
	require.False(t, res.IsError)
	return res.Content[0].Text
}

func TestMCPContext_DefaultDepth3_EachAncestorPromptOnce(t *testing.T) {
	id := seedDepth3Context(t)
	out := callMCPContext(t, `{"id":"`+id+`"}`)
	assertAncestorPromptsOnce(t, out, 1)
	p := decodeCtxPayload(t, out)
	require.Len(t, p.Chain, 4)
	assert.Empty(t, p.Chain[0].Prompt)
	assert.Contains(t, p.AssembledPrompt, ctxAnnotation)
	assert.LessOrEqual(t, float64(len(out)), 1.3*float64(len(p.AssembledPrompt)),
		"payload %d bytes vs assembled_prompt %d bytes", len(out), len(p.AssembledPrompt))
}

func TestMCPContext_ChainTrue_RestoresFullStructuredChain(t *testing.T) {
	id := seedDepth3Context(t)
	out := callMCPContext(t, `{"id":"`+id+`","chain":true}`)
	assertAncestorPromptsOnce(t, out, 2)
	p := decodeCtxPayload(t, out)
	assert.Equal(t, ctxPromptRoot, p.Chain[0].Prompt)
}

func TestMCPContext_Description_NamesChainOptIn(t *testing.T) {
	reg := mcp.NewToolRegistry()
	mcp.RegisterContextTools(reg, nil, nil)
	for _, td := range reg.List() {
		if td.Name == "mtix_context" {
			assert.Contains(t, td.Description, "chain")
			assert.Contains(t, td.InputSchema.Properties, "chain")
			return
		}
	}
	t.Fatal("mtix_context not registered")
}
