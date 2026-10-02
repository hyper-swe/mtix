// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
)

// providerNameRE matches third-party hosting and database provider names as
// whole words, case-insensitively. Shipped docs describe the capability and
// the mtix setting, never a named provider (project rule: provider-neutral
// agent-facing documentation, MTIX-95.8.2).
var providerNameRE = regexp.MustCompile(
	`(?i)\b(supabase|neon|aurora|rds|cloud\s+sql|alloydb|planetscale|cockroach(?:db|\s+cloud)?|heroku|railway|elephantsql|aiven|crunchy\s+bridge|timescale\s+cloud|gcs|google\s+cloud\s+storage|azure|aws)\b`)

// allowedProviderPhrase is the single allow-list entry: it names an API, not a
// vendor.
const allowedProviderPhrase = "S3-compatible"

// providerScanSkipPrefixes are historical records that are not instructions
// to an agent or operator (audit and incident write-ups).
var providerScanSkipPrefixes = []string{"docs/audit/", "docs/incidents/"}

// findProviderNames returns "file:line: match" for every provider name in
// the given documents.
func findProviderNames(docs map[string]string) []string {
	var hits []string
	for path, body := range docs {
		skip := false
		for _, p := range providerScanSkipPrefixes {
			if strings.HasPrefix(path, p) {
				skip = true
			}
		}
		if skip {
			continue
		}
		for i, line := range strings.Split(body, "\n") {
			line = strings.ReplaceAll(line, allowedProviderPhrase, "")
			if m := providerNameRE.FindString(line); m != "" {
				hits = append(hits, path+":"+strconv.Itoa(i+1)+": "+m)
			}
		}
	}
	sort.Strings(hits)
	return hits
}

func TestProviderNeutral_FinderFlagsNamesAsWholeWordsOnly(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"named provider", "use Supabase here", true},
		{"lowercase", "an rds instance", true},
		{"two words", "Cloud SQL proxy", true},
		{"substring of another word", "neonatal standards and boards", false},
		{"vendor cloud name", "upload with AWS or Azure", true},
		{"api name allowed", "an S3-compatible API", false},
		{"capability wording", "if your provider offers connection pooling", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findProviderNames(map[string]string{"x.md": tt.text})
			require.Equal(t, tt.want, len(got) > 0, "hits: %v", got)
		})
	}
}

func TestProviderNeutral_NoShippedDocNamesAHostingProvider(t *testing.T) {
	set := loadShippedDocs(t)
	docs := map[string]string{}
	for k, v := range set.all {
		docs[k] = v
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)
	docs["README.md"] = string(readme)

	hits := findProviderNames(docs)
	require.Empty(t, hits, "shipped documents name a hosting provider; describe the capability and the mtix setting instead")
}

// TestProviderNeutral_HubConnectionPartialIsSharedWithUserManual proves the
// one hub-connection text feeds both the generated docs and USERMANUAL.md, so
// they cannot disagree. The sync skill (MTIX-95.8.1) includes the same
// partial with {{ template "hub_connection" . }}.
func TestProviderNeutral_HubConnectionPartialIsSharedWithUserManual(t *testing.T) {
	tmpl, err := template.ParseFS(embeddedTemplates, "templates/partials/*.tmpl")
	require.NoError(t, err)
	var buf strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&buf, "hub_connection", nil))
	partial := strings.TrimSpace(buf.String())
	require.Contains(t, partial, "MTIX_SYNC_SSLROOTCERT")

	manual, err := os.ReadFile(filepath.Join("..", "..", "USERMANUAL.md"))
	require.NoError(t, err)
	require.Contains(t, string(manual), partial,
		"USERMANUAL.md must carry the hub_connection partial verbatim (edit templates/partials/hub_connection.md.tmpl and paste it)")

	set := loadShippedDocs(t)
	require.Contains(t, set.all["internal/docs/templates/workflows/small-team.md.tmpl"], `template "hub_connection"`)
}
