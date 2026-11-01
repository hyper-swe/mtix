// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// textSink matches a line that builds human-readable text; idCarrier matches
// a value that can be a pulled sync event id (an annotation id, a node uid
// or an event id).
var (
	textSink  = regexp.MustCompile(`Sprintf|Errorf|Fprintf|ErrorResult\(|SuccessResult\(|HandleValidationError\(`)
	idCarrier = regexp.MustCompile(`(?i)annotation_?id|\.UID\b|\bUID\b|EventID|InboxEvent|\bev(ent)?\.ID\b`)
)

// unrenderedCarrierLines returns the text-building lines of src that name an
// id carrier without routing it through format.EventID. It is a secondary,
// name-based check: the behavioral tests are the proof, this catches a new
// site that a reader can see.
func unrenderedCarrierLines(src string) []string {
	var out []string
	for i, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "//") {
			continue
		}
		if textSink.MatchString(line) && idCarrier.MatchString(line) && !strings.Contains(line, "format.EventID") {
			out = append(out, strings.TrimSpace(line)+" (line "+strconv.Itoa(i+1)+")")
		}
	}
	return out
}

// TestGuard_Matcher_FlagsRawCarriersAndAcceptsRendered pins the matcher with
// planted positive and negative snippets.
func TestGuard_Matcher_FlagsRawCarriersAndAcceptsRendered(t *testing.T) {
	bad := []string{
		`return SuccessResult(fmt.Sprintf("Annotation %s resolved", p.AnnotationID)), nil`,
		`msg := fmt.Sprintf("uid %s", node.UID)`,
		`Body: fmt.Sprintf("%+v", event.EventID)`,
		`return fmt.Errorf("event %s: %w", ev.EventID, err)`,
		`x := fmt.Sprintf("%v", inboxEvent)  // InboxEvent`,
	}
	good := []string{
		`return SuccessResult(fmt.Sprintf("Annotation %s resolved", format.EventID(p.AnnotationID))), nil`,
		`// SuccessResult(fmt.Sprintf("%s", p.AnnotationID)) in a comment`,
		`x := p.AnnotationID`,
		`return fmt.Errorf("parse args: %w", err)`,
	}
	for _, line := range bad {
		assert.NotEmpty(t, unrenderedCarrierLines(line), line)
	}
	for _, line := range good {
		assert.Empty(t, unrenderedCarrierLines(line), line)
	}
}

// TestGuard_MCPProduction_RendersEveryIDCarrierInText scans the production
// sources of the three API packages. It fails when it scans nothing, so a
// moved directory cannot turn it into a silent no-op.
func TestGuard_MCPProduction_RendersEveryIDCarrierInText(t *testing.T) {
	scanned := map[string]int{}
	for name, dir := range map[string]string{"mcp": ".", "http": "../api/http", "grpc": "../api/grpc"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		require.NoError(t, err)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f) // #nosec G304 -- fixed in-repo source paths
			require.NoError(t, err)
			scanned[name]++
			assert.Empty(t, unrenderedCarrierLines(string(src)), f)
		}
	}
	assert.GreaterOrEqual(t, scanned["mcp"], 20, "mcp files scanned")
	assert.GreaterOrEqual(t, scanned["http"], 10, "http files scanned")
	assert.GreaterOrEqual(t, scanned["grpc"], 2, "grpc files scanned")
}
