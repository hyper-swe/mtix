// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// MTIX-146: NodeFilter.Text matches every word against title, description or
// prompt with a parameterized, escaped LIKE.
func TestListNodes_TextFilter_MatchesWordsLiterally(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seed := []struct{ title, desc, prompt string }{
		{"Alpha template", "", ""},
		{"Beta", "has Gizmo", ""},
		{"Gamma", "", "100% of work_item"},
		{"Delta", "", `back\slash`},
	}
	for i, n := range seed {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("TX-%d", i+1), Project: "TX", Depth: 0, Seq: i + 1,
			Title: n.title, Description: n.desc, Prompt: n.prompt,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: fmt.Sprintf("h%d", i),
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	tests := []struct {
		name  string
		words []string
		want  []string
	}{
		{"title", []string{"template"}, []string{"TX-1"}},
		{"description case-insensitive", []string{"GIZMO"}, []string{"TX-2"}},
		{"prompt", []string{"work_item"}, []string{"TX-3"}},
		{"no match", []string{"xyzzynomatch"}, nil},
		{"words are ANDed", []string{"alpha", "gizmo"}, nil},
		{"percent literal", []string{"100%"}, []string{"TX-3"}},
		{"lone percent is literal", []string{"%"}, []string{"TX-3"}},
		{"underscore is not a wildcard", []string{"work?item"}, nil},
		{"backslash literal", []string{`k\s`}, []string{"TX-4"}},
		{"quote is data", []string{"x' OR '1'='1"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes, total, err := s.ListNodes(ctx, store.NodeFilter{Text: tt.words}, store.ListOptions{Limit: 50})
			require.NoError(t, err)
			var got []string
			for _, n := range nodes {
				got = append(got, n.ID)
			}
			assert.ElementsMatch(t, tt.want, got)
			assert.Equal(t, len(tt.want), total)
		})
	}
}
