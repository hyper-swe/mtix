// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// ContextOptions configures context chain assembly.
type ContextOptions struct {
	// MaxTokens limits the assembled prompt size per FR-12.4.
	// Zero means no limit.
	MaxTokens int
}

// ContextEntry represents a single node in the context chain per FR-12.2.
type ContextEntry struct {
	// ID is the dot-notation node ID.
	ID string `json:"id"`

	// Depth is the hierarchy depth.
	Depth int `json:"depth"`

	// Tier is derived from depth (story/epic/issue/micro).
	Tier model.NodeType `json:"tier"`

	// Title is the node title.
	Title string `json:"title"`

	// Description is included only for nodes within 2 levels of target per FR-12.2.
	Description string `json:"description,omitempty"`

	// Prompt is the node's prompt field.
	Prompt string `json:"prompt,omitempty"`

	// Status is the current node status.
	Status model.Status `json:"status"`

	// Creator is the author of the node, used for attribution classification.
	Creator string `json:"creator,omitempty"`

	// Annotations contains unresolved annotations only per requirement-prompts.md.
	Annotations []model.Annotation `json:"annotations,omitempty"`
}

// ContextResponse is the response from GetContext per FR-12.2/12.3.
type ContextResponse struct {
	// Chain is the ancestor chain from root to target (inclusive).
	Chain []ContextEntry `json:"chain"`

	// Siblings are the target node's siblings (same parent, excluding self).
	Siblings []ContextSibling `json:"siblings,omitempty"`

	// BlockingDeps are unresolved blocking dependencies on the target.
	BlockingDeps []*model.Dependency `json:"blocking_deps,omitempty"`

	// AssembledPrompt is the single coherent briefing per FR-12.3.
	AssembledPrompt string `json:"assembled_prompt"`
}

// ContextSibling is a lightweight representation of a sibling node.
type ContextSibling struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Status model.Status `json:"status"`
}

// ContextService assembles context chains for nodes per FR-12.2.
type ContextService struct {
	store  store.Store
	config ConfigProvider
	logger *slog.Logger
}

// NewContextService creates a ContextService with required dependencies.
func NewContextService(s store.Store, config ConfigProvider, logger *slog.Logger) *ContextService {
	if config == nil {
		config = &StaticConfig{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ContextService{
		store:  s,
		config: config,
		logger: logger,
	}
}

// GetContext assembles the full context chain for a node per FR-12.2/12.3.
// Walks ancestors root→target, includes prompts, annotations, siblings, blocking deps.
// Builds assembled_prompt with source attribution markers per FR-12.3a.
func (svc *ContextService) GetContext(
	ctx context.Context, nodeID string, opts *ContextOptions,
) (*ContextResponse, error) {
	ancestors, err := svc.store.GetAncestorChain(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get ancestor chain for %s: %w", nodeID, err)
	}

	if len(ancestors) == 0 {
		return nil, fmt.Errorf("no ancestors found for %s: %w", nodeID, model.ErrNotFound)
	}

	targetDepth := ancestors[len(ancestors)-1].Depth
	chain := svc.buildChain(ancestors, targetDepth)

	siblings, err := svc.getSiblings(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get siblings for %s: %w", nodeID, err)
	}

	blockingDeps, err := svc.store.GetBlockers(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get blockers for %s: %w", nodeID, err)
	}

	// Build assembled prompt, applying token truncation if configured.
	var assembled string
	if opts != nil && opts.MaxTokens > 0 {
		assembled = truncateAssembledPrompt(chain, opts.MaxTokens)
	} else {
		assembled = svc.assemblePrompt(chain, blockingDeps)
	}

	return &ContextResponse{
		Chain:           chain,
		Siblings:        siblings,
		BlockingDeps:    blockingDeps,
		AssembledPrompt: assembled,
	}, nil
}

// buildChain converts ancestor nodes into ContextEntry slices.
// Omits descriptions for nodes more than 2 levels from target per FR-12.2.
func (svc *ContextService) buildChain(ancestors []*model.Node, targetDepth int) []ContextEntry {
	entries := make([]ContextEntry, 0, len(ancestors))

	for _, node := range ancestors {
		entry := ContextEntry{
			ID:      node.ID,
			Depth:   node.Depth,
			Tier:    model.NodeTypeForDepth(node.Depth),
			Title:   node.Title,
			Prompt:  node.Prompt,
			Status:  node.Status,
			Creator: node.Creator,
		}

		// Include description only for nodes within 2 levels of target.
		if targetDepth-node.Depth <= 2 {
			entry.Description = node.Description
		}

		// Include only unresolved annotations per requirement-prompts.md.
		for _, a := range node.Annotations {
			if !a.Resolved {
				entry.Annotations = append(entry.Annotations, a)
			}
		}

		entries = append(entries, entry)
	}

	return entries
}

// getSiblings retrieves sibling summaries for the target node.
func (svc *ContextService) getSiblings(ctx context.Context, nodeID string) ([]ContextSibling, error) {
	nodes, err := svc.store.GetSiblings(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	siblings := make([]ContextSibling, 0, len(nodes))
	for _, n := range nodes {
		siblings = append(siblings, ContextSibling{
			ID:     n.ID,
			Title:  n.Title,
			Status: n.Status,
		})
	}

	return siblings, nil
}

// assemblePrompt builds the assembled_prompt string per FR-12.3a.
// Includes source attribution markers: [HUMAN-AUTHORED], [LLM-GENERATED],
// [ANNOTATION by {author}]. Classification based on creator matching agent.id_pattern.
func (svc *ContextService) assemblePrompt(
	chain []ContextEntry, blockingDeps []*model.Dependency,
) string {
	var sb strings.Builder

	for i, entry := range chain {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}

		fmt.Fprintf(&sb, "## %s %s: %s\n",
			strings.ToUpper(string(entry.Tier)), entry.ID, entry.Title)

		if entry.Prompt != "" {
			attribution := classifyCreator(entry.Creator)
			fmt.Fprintf(&sb, "\n%s\n%s\n", attribution, entry.Prompt)
		}

		for _, a := range entry.Annotations {
			fmt.Fprintf(&sb, "\n[ANNOTATION by %s]\n%s\n", a.Author, a.Text)
		}
	}

	if len(blockingDeps) > 0 {
		sb.WriteString("\n\n---\n\n## Blocking Dependencies\n")
		for _, dep := range blockingDeps {
			fmt.Fprintf(&sb, "- %s blocks this node\n", dep.FromID)
		}
	}

	return sb.String()
}

// classifyCreator determines the attribution marker for a node's creator per FR-12.3a.
// Agent IDs matching the "agent-" prefix pattern are classified as LLM-GENERATED.
// All others are classified as HUMAN-AUTHORED.
func classifyCreator(creator string) string {
	if strings.HasPrefix(creator, agentIDPattern) {
		return "[LLM-GENERATED]"
	}
	return "[HUMAN-AUTHORED]"
}

// agentIDPattern is the default prefix for identifying LLM agents per FR-12.3a.
const agentIDPattern = "agent-"
