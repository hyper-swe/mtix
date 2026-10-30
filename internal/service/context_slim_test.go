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

func TestContextResponse_Slim_KeepsStructureAndDescriptionDropsPromptCreatorAnnotations(t *testing.T) {
	ctxSvc, nodeSvc, _ := newTestContextService(t)
	_, _, issue := createChain(t, nodeSvc)

	full, err := ctxSvc.GetContext(context.Background(), issue.ID, nil)
	require.NoError(t, err)
	slim := full.Slim()

	require.Len(t, slim.Chain, len(full.Chain))
	for i, e := range slim.Chain {
		assert.Equal(t, full.Chain[i].ID, e.ID)
		assert.Equal(t, full.Chain[i].Depth, e.Depth)
		assert.Equal(t, full.Chain[i].Tier, e.Tier)
		assert.Equal(t, full.Chain[i].Title, e.Title)
		assert.Equal(t, full.Chain[i].Status, e.Status)
		assert.Equal(t, full.Chain[i].Description, e.Description, "description is kept")
		assert.Empty(t, e.Prompt)
		assert.Empty(t, e.Creator)
		assert.Empty(t, e.Annotations)
	}
	assert.Equal(t, full.AssembledPrompt, slim.AssembledPrompt)
	assert.Equal(t, full.Siblings, slim.Siblings)
	assert.NotEmpty(t, full.Chain[0].Prompt, "receiver is not modified")
}

func TestContextResponse_Slim_KeepsBlockingDeps(t *testing.T) {
	ctxSvc, nodeSvc, s := newTestContextService(t)
	ctx := context.Background()
	_, _, issue := createChain(t, nodeSvc)
	blocker, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Blocker", Creator: "admin",
	})
	require.NoError(t, err)
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: blocker.ID, ToID: issue.ID, DepType: model.DepTypeBlocks,
		CreatedAt: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), CreatedBy: "admin",
	}))

	full, err := ctxSvc.GetContext(ctx, issue.ID, nil)
	require.NoError(t, err)
	require.Len(t, full.BlockingDeps, 1)
	slim := full.Slim()
	require.Len(t, slim.BlockingDeps, 1)
	assert.Equal(t, blocker.ID, slim.BlockingDeps[0].FromID)
	assert.Contains(t, slim.AssembledPrompt, blocker.ID)
}
