// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncEventIDReach matches production code that could carry a sync event id
// into REST, gRPC or MCP text: event id fields, and the sync service and
// quarantine entry points.
var syncEventIDReach = regexp.MustCompile(`(?i)event_?id|SyncService\b|AcknowledgeQuarantine|ListQuarantined|QuarantinedEvent`)

// TestAPISurfaces_ProductionCode_NeverReachesSyncEventIDs proves that no
// REST, gRPC or MCP production source can put a sync event id in its text:
// the only sync entry is the workflow interface, which returns counts and
// state. A future change that adds a path must route the id through
// format.EventID and update this test (MTIX-187.4.5).
func TestAPISurfaces_ProductionCode_NeverReachesSyncEventIDs(t *testing.T) {
	allowed := map[string]bool{
		// Documentation comment naming the production implementer of the
		// workflow interface; the tool returns counts only.
		"mcp/tools_sync_workflow.go": true,
	}
	for _, dir := range []string{"http", "grpc", "../mcp"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		require.NotEmpty(t, files)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f) // #nosec G304 -- fixed in-repo source paths
			require.NoError(t, err)
			if allowed[strings.TrimPrefix(filepath.ToSlash(f), "../")] {
				continue
			}
			assert.False(t, syncEventIDReach.Match(src), "%s can reach a sync event id", f)
		}
	}
}
