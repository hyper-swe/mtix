// Windows SQLite path runtime contract (MTIX-112.2.4): the job opens, writes,
// reopens read-only, verifies and recovers databases on a real Windows
// filesystem and requires each named test to execute and PASS, so the file: URI
// form of a drive path is proven there and not only by the host-OS-agnostic table.
package mtix_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const windowsSQLitePathStepName = "Run required native SQLite path tests"
const windowsSQLitePathFilter = "^Test(FileURIPathFor_OSParameterised|New_SpecialCharDir_OpensExactFile|Backup_SpecialCharDir_VerifiesExactFile|VerifyDatabase_SpecialCharDirMissing_NeverCreates|Recover_SpecialCharDir_SalvagesExactFile|CountUnackedInboxFile_SpecialCharDir_ReadsExactFile|OpenDB_ReadOnlyMissingPath_NeverCreates|New_TempDirPath_WriteReopenReadOnlyVerifyRecover)$"

func windowsSQLitePathNames() []string {
	return []string{"TestFileURIPathFor_OSParameterised", "TestNew_SpecialCharDir_OpensExactFile", "TestBackup_SpecialCharDir_VerifiesExactFile", "TestVerifyDatabase_SpecialCharDirMissing_NeverCreates", "TestRecover_SpecialCharDir_SalvagesExactFile", "TestCountUnackedInboxFile_SpecialCharDir_ReadsExactFile", "TestOpenDB_ReadOnlyMissingPath_NeverCreates", "TestNew_TempDirPath_WriteReopenReadOnlyVerifyRecover"}
}

func windowsSQLitePathScript() string {
	return strings.NewReplacer("@FILTER@", windowsSQLitePathFilter, "@NAMES@", windowsRuntimeQuotedNames(windowsSQLitePathNames())).Replace(windowsSQLitePathTemplate)
}

const windowsSQLitePathTemplate = `$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
& go test ./internal/store/sqlite -race -count=1 -timeout=5m -json -run '@FILTER@' > "$env:MTIX_WINDOWS_CAPTURE_DIR/sqlite-paths.json"
$sqlitePathExit = $LASTEXITCODE
$requiredSqlitePath = @NAMES@
$package = 'github.com/hyper-swe/mtix/internal/store/sqlite'
$events = Get-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/sqlite-paths.json" | ForEach-Object { $_ | ConvertFrom-Json }
$badEvents = @($events | Where-Object { $_.Package -eq $package -and ($_.Action -eq 'fail' -or $_.Action -eq 'skip') })
if ($badEvents.Count -ne 0) { throw "Native SQLite path FAIL or SKIP: $package" }
foreach ($test in $requiredSqlitePath) {
  $ran = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'run' })
  $passed = @($events | Where-Object { $_.Package -eq $package -and $_.Test -eq $test -and $_.Action -eq 'pass' })
  if ($ran.Count -ne 1 -or $passed.Count -ne 1) { throw "Required native SQLite path test did not execute and PASS: $package/$test" }
}
$packagePassed = @($events | Where-Object { $_.Package -eq $package -and !$_.Test -and $_.Action -eq 'pass' })
if ($packagePassed.Count -ne 1) { throw "Required native SQLite path package did not PASS: $package" }
if ($sqlitePathExit -ne 0) { throw "Native SQLite path test failure: sqlite-paths=$sqlitePathExit" }
`

// The actual workflow runs the SQLite path tests in one step that names them all.
func TestCI_WindowsSQLitePathWorkflow_ActualStepRunsEveryTest(t *testing.T) {
	src, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(src, &workflow))
	runs := []string{}
	for _, step := range workflow.Jobs["windows-runtime"].Steps {
		if step.Name == windowsSQLitePathStepName {
			runs = append(runs, step.Run)
		}
	}
	require.Len(t, runs, 1, "exactly one SQLite path step")
	require.Equal(t, windowsSQLitePathScript(), runs[0])
	for _, name := range windowsSQLitePathNames() {
		require.Contains(t, runs[0], name)
	}
}

// Each name the step requires must exist as a test in the package it runs.
func TestCI_WindowsSQLitePathWorkflow_NamedTestsExist(t *testing.T) {
	var src strings.Builder
	for _, file := range []string{"dsn_windows_path_test.go", "special_path_dirs_test.go"} {
		b, err := os.ReadFile("internal/store/sqlite/" + file)
		require.NoError(t, err)
		src.Write(b)
	}
	for _, name := range windowsSQLitePathNames() {
		require.Contains(t, src.String(), "func "+name+"(t *testing.T)")
	}
}
