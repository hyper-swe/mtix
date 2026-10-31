// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
)

// RegisterContextTools registers context and prompt MCP tools per MTIX-6.2.3.
func RegisterContextTools(reg *ToolRegistry, ctxSvc *service.ContextService, promptSvc *service.PromptService) {
	registerContextTool(reg, ctxSvc)
	registerPromptTool(reg, promptSvc)
	registerAnnotateTool(reg, promptSvc)
	registerResolveAnnotationTool(reg, promptSvc)
}

func registerContextTool(reg *ToolRegistry, svc *service.ContextService) {
	reg.Register(ToolDef{
		Name:        "mtix_context",
		Description: "Assemble context chain for a node (ancestors, siblings, blocking deps, prompt). Ancestor prompts and unresolved annotations appear once, in assembled_prompt; the structured chain lists id, depth, tier, title, status and description (no prompt, creator or annotations). Set chain:true to also get each ancestor's prompt, creator and annotations in the structured chain (the prompts then appear twice).",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":         {Type: "string", Description: "Node ID"},
				"max_tokens": {Type: "number", Description: "Maximum token budget for assembled prompt (0=unlimited)"},
				"chain":      {Type: "boolean", Description: "Include the full structured ancestor chain (adds prompt, creator and annotations per entry). Default false: those appear once in assembled_prompt"},
			},
			Required: []string{"id"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID        string `json:"id"`
			MaxTokens int    `json:"max_tokens"`
			Chain     bool   `json:"chain"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse context args: %w", err)
		}

		opts := &service.ContextOptions{MaxTokens: p.MaxTokens}
		resp, err := svc.GetContext(ctx, p.ID, opts)
		if err != nil {
			return nil, err
		}

		// MTIX-225: ancestor content appears once (assembled_prompt); the full
		// structured chain is opt-in via chain:true.
		if !p.Chain {
			resp = resp.Slim()
		}
		data, _ := json.MarshalIndent(resp, "", "  ")
		return SuccessResult(string(data)), nil
	})
}

func registerPromptTool(reg *ToolRegistry, svc *service.PromptService) {
	reg.Register(ToolDef{
		Name:        "mtix_prompt",
		Description: "Update a node's prompt text",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":     {Type: "string", Description: "Node ID"},
				"text":   {Type: "string", Description: "New prompt text"},
				"author": {Type: "string", Description: "Author of the change"},
			},
			Required: []string{"id", "text", "author"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID     string `json:"id"`
			Text   string `json:"text"`
			Author string `json:"author"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse prompt args: %w", err)
		}

		if err := svc.UpdatePrompt(ctx, p.ID, p.Text, p.Author); err != nil {
			return nil, err
		}

		return SuccessResult(textPromptUpdated(p.ID)), nil
	})
}

func registerAnnotateTool(reg *ToolRegistry, svc *service.PromptService) {
	reg.Register(ToolDef{
		Name:        "mtix_annotate",
		Description: "Add a comment annotation to a node's prompt. Set 'to' to address it at an agent, which delivers it to that agent's inbox (see mtix_inbox).",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":     {Type: "string", Description: "Node ID"},
				"text":   {Type: "string", Description: "Annotation text"},
				"author": {Type: "string", Description: "Author of the annotation"},
				"to":     {Type: "string", Description: "Address the comment at an agent; delivers to its inbox (empty for an ordinary comment)"},
			},
			Required: []string{"id", "text", "author"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID     string `json:"id"`
			Text   string `json:"text"`
			Author string `json:"author"`
			To     string `json:"to"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse annotate args: %w", err)
		}

		if err := svc.AddAnnotation(ctx, p.ID, p.Text, p.Author, p.To); err != nil {
			return nil, err
		}

		return SuccessResult(textAnnotationAdded(p.ID, p.To)), nil
	})
}

func registerResolveAnnotationTool(reg *ToolRegistry, svc *service.PromptService) {
	reg.Register(ToolDef{
		Name:        "mtix_resolve_annotation",
		Description: "Resolve an annotation on a node",
		InputSchema: SchemaObj{
			Type: "object",
			Properties: map[string]SchemaProp{
				"id":            {Type: "string", Description: "Node ID"},
				"annotation_id": {Type: "string", Description: "Annotation id to resolve: the full id or an unambiguous prefix of at least 12 characters"},
				"author":        {Type: "string", Description: "Author resolving the annotation"},
			},
			Required: []string{"id", "annotation_id", "author"},
		},
	}, func(ctx context.Context, args json.RawMessage) (*ToolsCallResult, error) {
		var p struct {
			ID           string `json:"id"`
			AnnotationID string `json:"annotation_id"`
			Author       string `json:"author"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("parse resolve annotation args: %w", err)
		}

		fullID, err := svc.ResolveAnnotationRef(ctx, p.ID, p.AnnotationID, true)
		if err != nil {
			return nil, err
		}

		return SuccessResult(textAnnotationResolved(format.EventID(fullID), p.ID)), nil
	})
}
