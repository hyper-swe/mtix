// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// seedAnnotations stores annotations with chosen ids on a fresh node.
func seedAnnotations(t *testing.T, ids ...string) (*service.PromptService, string, func() []model.Annotation) {
	t.Helper()
	promptSvc, nodeSvc, s, _ := newTestPromptService(t)
	ctx := context.Background()
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Refs", Creator: "admin"})
	require.NoError(t, err)
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for i, id := range ids {
		require.NoError(t, s.AppendAnnotation(ctx, node.ID, model.Annotation{
			ID: id, Author: "agent", Text: "note", CreatedAt: at.Add(time.Duration(i) * time.Second),
		}))
	}
	return promptSvc, node.ID, func() []model.Annotation {
		got, getErr := s.GetNode(ctx, node.ID)
		require.NoError(t, getErr)
		return got.Annotations
	}
}

func resolvedIDs(anns []model.Annotation) map[string]bool {
	out := map[string]bool{}
	for _, a := range anns {
		out[a.ID] = a.Resolved
	}
	return out
}

func TestResolveAnnotationRef_References_ResolveOrRefuse(t *testing.T) {
	const (
		idA = "01ARZ3NDEKTSV4RRFFQ69G5FAA"
		idB = "01ARZ3NDEKTSV4RRFFQ69G5FAB"
		idC = "01BX5ZZKBKACTAV9WEVGEMMVRZ"
		// idShort is itself a prefix of idLong, to prove that an exact match wins.
		idShort = "01CCCCCCCCCCCCCC"
		idLong  = "01CCCCCCCCCCCCCCDDDDDDDDDD"
	)
	tests := []struct {
		name     string
		ref      string
		wantID   string
		wantErr  error
		wantText []string // substrings the error must contain
	}{
		{"full id", idC, idC, nil, nil},
		{"unambiguous prefix of the short length", "01BX5ZZKBKAC", idC, nil, nil},
		{"unambiguous longer prefix", "01ARZ3NDEKTSV4RRFFQ69G5FAB", idB, nil, nil},
		{"exact match wins over a longer id sharing the prefix", idShort, idShort, nil, nil},
		{"ambiguous prefix lists the full ids", "01ARZ3NDEKTS", "", model.ErrInvalidInput,
			[]string{`"` + idA + `"`, `"` + idB + `"`}},
		{"too short but a prefix of an id", "01BX5ZZKBKA", "", model.ErrInvalidInput, []string{"at least 12"}},
		{"too short and matching nothing", "NONEXISTENT", "", model.ErrNotFound, nil},
		{"empty", "", "", model.ErrInvalidInput, nil},
		{"no match", "01ZZZZZZZZZZ", "", model.ErrNotFound, nil},
		{"bidi override in the typed reference is rendered safely", "01ZZ\u202eZZZZZZZZZZ", "", model.ErrNotFound, []string{`\u202e`}},
		{"hostile reference is rendered safely", "01ZZ\x1b[2J\nZZZZZZZZ", "", model.ErrNotFound, []string{`\x1b`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, nodeID, read := seedAnnotations(t, idA, idB, idC, idShort, idLong)
			gotID, err := svc.ResolveAnnotationRef(context.Background(), nodeID, tt.ref, true)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				for _, s := range tt.wantText {
					assert.Contains(t, err.Error(), s)
				}
				assert.NotContains(t, err.Error(), "\x1b")
				assert.NotContains(t, err.Error(), "\n")
				for id, resolved := range resolvedIDs(read()) {
					assert.False(t, resolved, "a refused reference must resolve nothing: %s", id)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, gotID)
			for id, resolved := range resolvedIDs(read()) {
				assert.Equal(t, id == tt.wantID, resolved, id)
			}
		})
	}
}

// TestResolveAnnotationRef_BidiInStoredIDs_ListsThemEscaped keeps raw bidi
// controls out of the ambiguity error that lists stored ids.
func TestResolveAnnotationRef_BidiInStoredIDs_ListsThemEscaped(t *testing.T) {
	svc, nodeID, _ := seedAnnotations(t, "01ARZ3NDEKTS\u202eAA", "01ARZ3NDEKTS\u202eAB")
	_, err := svc.ResolveAnnotationRef(context.Background(), nodeID, "01ARZ3NDEKTS", true)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.NotContains(t, err.Error(), string(rune(0x202e)))
	assert.Contains(t, err.Error(), `\u202e`)
}

// TestResolveAnnotationRef_EmptyReferenceOnNodeWithoutAnnotations_IsInvalid
// pins the empty-reference rule when there is nothing to match against.
func TestResolveAnnotationRef_EmptyReferenceOnNodeWithoutAnnotations_IsInvalid(t *testing.T) {
	svc, nodeID, _ := seedAnnotations(t)
	_, err := svc.ResolveAnnotationRef(context.Background(), nodeID, "", true)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestResolveAnnotation_PrefixReference_ResolvesThroughSameRule(t *testing.T) {
	svc, nodeID, read := seedAnnotations(t, "01BX5ZZKBKACTAV9WEVGEMMVRZ")
	require.NoError(t, svc.ResolveAnnotation(context.Background(), nodeID, "01BX5ZZKBKAC", true))
	assert.True(t, read()[0].Resolved)
}

func TestResolveAnnotationRef_UnknownNode_ReturnsNotFound(t *testing.T) {
	svc, _, _ := seedAnnotations(t, "01BX5ZZKBKACTAV9WEVGEMMVRZ")
	_, err := svc.ResolveAnnotationRef(context.Background(), "PROJ-999", "01BX5ZZKBKAC", true)
	require.ErrorIs(t, err, model.ErrNotFound)
}
