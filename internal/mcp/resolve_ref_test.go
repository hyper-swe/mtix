// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type fakeResolver struct {
	byID    map[string]*model.Node
	uidToID map[string]string
}

func (f *fakeResolver) GetNode(_ context.Context, id string) (*model.Node, error) {
	if n, ok := f.byID[id]; ok {
		return n, nil
	}
	return nil, fmt.Errorf("node %s: %w", id, model.ErrNotFound)
}

func (f *fakeResolver) ResolveDisplayPathByUID(_ context.Context, uid string) (string, error) {
	if id, ok := f.uidToID[uid]; ok {
		return id, nil
	}
	return "", fmt.Errorf("uid %s: %w", uid, model.ErrNotFound)
}

const sampleUID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

func newFakeResolver() *fakeResolver {
	return &fakeResolver{
		byID:    map[string]*model.Node{"PROJ-1.5": {ID: "PROJ-1.5", UID: sampleUID}},
		uidToID: map[string]string{sampleUID: "PROJ-1.5"},
	}
}

func TestResolveNodeRef_DisplayPath(t *testing.T) {
	got, err := resolveNodeRef(context.Background(), newFakeResolver(), "PROJ-1.5")
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1.5", got.ID)
}

func TestResolveNodeRef_ByUID_SurvivesRenumber(t *testing.T) {
	got, err := resolveNodeRef(context.Background(), newFakeResolver(), sampleUID)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1.5", got.ID)
}

func TestResolveNodeRef_UnknownDisplayPath(t *testing.T) {
	_, err := resolveNodeRef(context.Background(), newFakeResolver(), "PROJ-9.9")
	require.ErrorIs(t, err, model.ErrNotFound)
}

func TestResolveNodeRef_UnknownUID(t *testing.T) {
	_, err := resolveNodeRef(context.Background(), newFakeResolver(),
		"0190ffff-ffff-7fff-8fff-ffffffffffff")
	require.ErrorIs(t, err, model.ErrNotFound)
}

func TestResolveNodeRef_EmptyRef(t *testing.T) {
	_, err := resolveNodeRef(context.Background(), newFakeResolver(), "")
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
