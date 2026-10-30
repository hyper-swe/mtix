// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextResponse_Slim_KeepsStructureDropsContent(t *testing.T) {
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
		assert.Empty(t, e.Prompt)
		assert.Empty(t, e.Description)
		assert.Empty(t, e.Creator)
		assert.Empty(t, e.Annotations)
	}
	assert.Equal(t, full.AssembledPrompt, slim.AssembledPrompt)
	assert.Equal(t, full.Siblings, slim.Siblings)
	assert.NotEmpty(t, full.Chain[0].Prompt, "receiver is not modified")
}
