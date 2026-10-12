// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Controls use actual temporary Go files and parsed workflow literals, with no
// dependency on the existing workflow's expected-name fixture or regex constants.
package mtix_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const windowsAvailabilityFixtureRun = `& go test ./native -race -count=1 -timeout=8m -json -run '^Test(Required|Optional)$' > "$env:MTIX_WINDOWS_CAPTURE_DIR/native.json"
& go test ./hooks -race -count=1 -timeout=8m -json -run '^TestHook$' > "$env:MTIX_WINDOWS_CAPTURE_DIR/hooks.json"
$requiredNative = @('TestRequired')
$requiredHooks = @('TestHook')
foreach ($suite in @(@{ file='native.json'; package='example.test/windows/native'; names=$requiredNative }, @{ file='hooks.json'; package='example.test/windows/hooks'; names=$requiredHooks })) {
  if ($package -eq 'example.test/windows/native') {
    $optionalAlias = 'TestOptional'
  }
}
`

const windowsAvailabilityRequiredSource = `//go:build windows && amd64 && cgo && race

package fixture
import test "testing"
func TestRequired(t *test.T) {}
`

func windowsAvailabilityFixtureRoot(t *testing.T, name, source string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                  "module example.test/windows\n",
		"native/" + name:          source,
		"native/optional_test.go": "package fixture\nimport \"testing\"\nfunc TestOptional(t *testing.T) {}\n",
		"hooks/hook_test.go":      "package fixture\nimport \"testing\"\nfunc TestHook(t *testing.T) {}\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	}
	return root
}

func windowsAvailabilityFixtureYAML(t *testing.T, run string) []byte {
	t.Helper()
	wf := map[string]any{"jobs": map[string]any{"test": map[string]any{"steps": []any{map[string]any{"name": "Run required native ACL and hooks status tests", "run": run}}}}}
	src, err := yaml.Marshal(wf)
	require.NoError(t, err)
	return src
}

func TestCI_WindowsRuntimeAvailability_BuildConstraintsAndASTControls(t *testing.T) {
	cases := []struct {
		name, file, source string
		wantError          bool
	}{
		{"Windows amd64 CGO race included", "required_windows_test.go", windowsAvailabilityRequiredSource, false},
		{"unconstrained included", "required_test.go", strings.Replace(windowsAvailabilityRequiredSource, "//go:build windows && amd64 && cgo && race\n\n", "", 1), false},
		{"dot testing import included", "required_windows_test.go", strings.Replace(strings.Replace(windowsAvailabilityRequiredSource, `import test "testing"`, `import . "testing"`, 1), "*test.T", "*T", 1), false},
		{"not Windows excluded", "required_test.go", strings.Replace(windowsAvailabilityRequiredSource, "windows && amd64 && cgo && race", "!windows", 1), true},
		{"Linux filename excluded", "required_linux_test.go", strings.Replace(windowsAvailabilityRequiredSource, "//go:build windows && amd64 && cgo && race\n\n", "", 1), true},
		{"arm64 excluded", "required_test.go", strings.Replace(windowsAvailabilityRequiredSource, "amd64", "arm64", 1), true},
		{"CGO disabled excluded", "required_test.go", strings.Replace(windowsAvailabilityRequiredSource, "&& cgo", "&& !cgo", 1), true},
		{"race disabled excluded", "required_test.go", strings.Replace(windowsAvailabilityRequiredSource, "&& race", "&& !race", 1), true},
		{"absent declaration", "required_test.go", "package fixture\n", true},
		{"comment decoy", "required_test.go", "package fixture\n// func TestRequired(t *testing.T) {}\n", true},
		{"string decoy", "required_test.go", "package fixture\nconst decoy = \"func TestRequired(t *testing.T) {}\"\n", true},
		{"wrong argument type", "required_windows_test.go", strings.Replace(windowsAvailabilityRequiredSource, "*test.T", "*test.B", 1), true},
		{"fake testing import", "required_windows_test.go", strings.Replace(windowsAvailabilityRequiredSource, `"testing"`, `"fake/testing"`, 1), true},
		{"method is not a top-level test", "required_windows_test.go", strings.Replace(windowsAvailabilityRequiredSource, "func TestRequired", "type receiver struct{}\nfunc (receiver) TestRequired", 1), true},
		{"malformed included source", "required_windows_test.go", "package fixture\nfunc TestRequired(\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := windowsAvailabilityFixtureRoot(t, tc.file, tc.source)
			err := windowsActualAvailabilityError(root, windowsAvailabilityFixtureYAML(t, windowsAvailabilityFixtureRun))
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCI_WindowsRuntimeAvailability_ActualLiteralControls(t *testing.T) {
	cases := []struct{ name, old, replacement string }{
		{"required test not selected", "^Test(Required|Optional)$", "^TestOptional$"},
		{"optional alias not selected", "^Test(Required|Optional)$", "^TestRequired$"},
		{"wrong package binding", "package='example.test/windows/native'", "package='example.test/windows/wrong'"},
		{"absent required test", "$requiredNative = @('TestRequired')", "$requiredNative = @('TestAbsent')"},
		{"empty required manifest", "$requiredNative = @('TestRequired')", "$requiredNative = @()"},
		{"omitted required manifest", "$requiredNative = @('TestRequired')\n", ""},
		{"selected included test ungated", "$requiredNative = @('TestRequired')", "$requiredNative = @('TestOptional')"},
		{"optional declaration disappeared", "$optionalAlias = 'TestOptional'", "$optionalAlias = ''"},
		{"optional unavailable name", "$optionalAlias = 'TestOptional'", "$optionalAlias = 'TestAbsent'"},
		{"optional wrong package", "if ($package -eq 'example.test/windows/native')", "if ($package -eq 'example.test/windows/wrong')"},
		{"duplicate manifest", "$requiredNative = @('TestRequired')", "$requiredNative = @('TestRequired')\n$requiredNative = @('TestRequired')"},
		{"duplicate required name", "$requiredNative = @('TestRequired')", "$requiredNative = @('TestRequired', 'TestRequired')"},
		{"required manifest bound twice", "names=$requiredHooks", "names=$requiredNative"},
		{"unknown manifest binding", "names=$requiredNative", "names=$requiredUnknown"},
		{"comment invocation decoy", "& go test ./native", "# & go test ./native"},
		{"duplicate invocation", "& go test ./native", "& go test ./hooks -race -count=1 -timeout=8m -json -run '^TestHook$' > \"$env:MTIX_WINDOWS_CAPTURE_DIR/hooks.json\"\n& go test ./native"},
	}
	root := windowsAvailabilityFixtureRoot(t, "required_windows_test.go", windowsAvailabilityRequiredSource)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, windowsAvailabilityFixtureRun, tc.old)
			run := strings.Replace(windowsAvailabilityFixtureRun, tc.old, tc.replacement, 1)
			require.Error(t, windowsActualAvailabilityError(root, windowsAvailabilityFixtureYAML(t, run)))
		})
	}
}
