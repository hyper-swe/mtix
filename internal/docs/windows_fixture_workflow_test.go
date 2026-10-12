// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fixture workflow guards inspect actual parsed YAML and executable selection.
// They are independent of the root package's expected workflow model and do
// not establish Windows execution or Windows-buildable test availability.
package docs

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const stockReplayTestName = "TestAncestor_CapturedStockWindowsFixture_ReplaysPolicy"
const stockLiveCaptureTestName = "TestAncestor_StockWindowsAclCapture_AcceptsPolicy"
const stockNativeTestStepName = "Run required native ACL and hooks status tests"

func TestWindowsFixtureWorkflow_ActualSource_RequiresReplayAndLiveCapture(t *testing.T) {
	source, err := os.ReadFile("../../.github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	require.NoError(t, stockFixtureWorkflowError(source))
	require.NoError(t, namedLockWorkflowError(source, "../.."))
}

func stockFixtureWorkflowError(source []byte) error {
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		return fmt.Errorf("parse fixture workflow YAML: %w", err)
	}
	var scripts []string
	for _, step := range workflow.Jobs["windows-runtime"].Steps {
		if step.Name == stockNativeTestStepName {
			scripts = append(scripts, step.Run)
		}
	}
	if len(scripts) != 1 {
		return fmt.Errorf("fixture workflow must contain one named native test step")
	}
	return stockFixtureScriptError(scripts[0])
}

func stockFixtureScriptError(script string) error {
	selector := regexp.MustCompile(`^& go test ./internal/oplocal -race -count=1 -timeout=8m -json -run '([^']+)' > "\$env:MTIX_WINDOWS_CAPTURE_DIR/oplocal.json"$`)
	manifest := regexp.MustCompile(`^\$requiredOplocal = @\('([A-Za-z0-9_]+)'(?:, '[A-Za-z0-9_]+')*\)$`)
	var selections, manifests []string
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if match := selector.FindStringSubmatch(line); match != nil {
			selections = append(selections, match[1])
		}
		if manifest.MatchString(line) {
			manifests = append(manifests, line)
		}
	}
	if len(selections) != 1 || len(manifests) != 1 {
		return fmt.Errorf("fixture workflow requires one executable oplocal selector and one requiredOplocal manifest")
	}
	selected, err := stockFixtureSelectedNames(selections[0])
	if err != nil {
		return err
	}
	var failures []error
	for _, name := range []string{stockReplayTestName, stockLiveCaptureTestName} {
		if selected[name] != 1 {
			failures = append(failures, fmt.Errorf("native selector must select %s exactly once", name))
		}
		if strings.Count(manifests[0], "'"+name+"'") != 1 {
			failures = append(failures, fmt.Errorf("requiredOplocal PASS manifest must require %s exactly once", name))
		}
	}
	return errors.Join(failures...)
}

func stockFixtureSelectedNames(selector string) (map[string]int, error) {
	if !strings.HasPrefix(selector, "^Test(") || !strings.HasSuffix(selector, ")$") {
		return nil, fmt.Errorf("fixture selector must be an anchored literal test-name alternation")
	}
	namePattern := regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	names := map[string]int{}
	for _, branch := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(selector, "^Test("), ")$"), "|") {
		if !namePattern.MatchString(branch) {
			return nil, fmt.Errorf("fixture selector contains nonliteral test name %q", branch)
		}
		names["Test"+branch]++
	}
	return names, nil
}

// SYNTHETIC minimal YAML is a guard control, never a workflow implementation.
func syntheticStockFixtureWorkflow(t *testing.T, script string) []byte {
	t.Helper()
	workflow := map[string]any{"jobs": map[string]any{"windows-runtime": map[string]any{
		"steps": []any{map[string]any{"name": stockNativeTestStepName, "run": script}},
	}}}
	source, err := yaml.Marshal(workflow)
	require.NoError(t, err)
	return source
}

func syntheticStockFixtureScript() string {
	return "& go test ./internal/oplocal -race -count=1 -timeout=8m -json -run '^Test(" +
		strings.TrimPrefix(stockReplayTestName, "Test") + "|" + strings.TrimPrefix(stockLiveCaptureTestName, "Test") +
		")$' > \"$env:MTIX_WINDOWS_CAPTURE_DIR/oplocal.json\"\n" +
		"$requiredOplocal = @('" + stockReplayTestName + "', '" + stockLiveCaptureTestName + "')\n"
}

func TestWindowsFixtureWorkflow_SYNTHETICControls_RejectMissingSelectorsAndManifest(t *testing.T) {
	valid := syntheticStockFixtureScript()
	replayBranch := strings.TrimPrefix(stockReplayTestName, "Test")
	liveBranch := strings.TrimPrefix(stockLiveCaptureTestName, "Test")
	tests := []struct {
		name   string
		script string
		want   string
	}{
		{"valid minimal contract", valid, ""},
		{"replay selector missing", strings.Replace(valid, replayBranch+"|", "", 1), "native selector must select " + stockReplayTestName},
		{"replay PASS missing", strings.Replace(valid, "'"+stockReplayTestName+"', ", "", 1), "PASS manifest must require " + stockReplayTestName},
		{"live selector missing", strings.Replace(valid, "|"+liveBranch, "", 1), "native selector must select " + stockLiveCaptureTestName},
		{"live PASS missing", strings.Replace(valid, ", '"+stockLiveCaptureTestName+"'", "", 1), "PASS manifest must require " + stockLiveCaptureTestName},
		{"duplicate replay selection", strings.Replace(valid, replayBranch, replayBranch+"|"+replayBranch, 1), "exactly once"},
		{"duplicate replay manifest", strings.Replace(valid, "'"+stockReplayTestName+"'", "'"+stockReplayTestName+"', '"+stockReplayTestName+"'", 1), "exactly once"},
		{"commented selector decoy", strings.Replace(valid, "& go test", "# & go test", 1), "one executable"},
		{"commented manifest decoy", strings.Replace(valid, "$requiredOplocal", "# $requiredOplocal", 1), "one executable"},
		{"unanchored selector", strings.Replace(valid, "^Test(", "Test(", 1), "anchored literal"},
		{"broad regex branch", strings.Replace(valid, replayBranch, "Ancestor_.*", 1), "nonliteral"},
		{"renamed replay selector", strings.Replace(valid, replayBranch, replayBranch+"Decoy", 1), "native selector must select"},
		{"duplicate executable invocation", valid + strings.Split(valid, "\n")[0] + "\n", "one executable"},
		{"duplicate manifest assignment", valid + strings.Split(valid, "\n")[1] + "\n", "one executable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := stockFixtureWorkflowError(syntheticStockFixtureWorkflow(t, tt.script))
			if tt.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
}

func TestWindowsFixtureWorkflow_SYNTHETICYAMLControls_RejectInvalidSource(t *testing.T) {
	tests := []struct{ name, source string }{
		{"empty", ""},
		{"malformed", "jobs: ["},
		{"missing job", "jobs: {}"},
		{"missing step", "jobs:\n  windows-runtime:\n    steps: []"},
		{"wrong step", "jobs:\n  windows-runtime:\n    steps:\n      - name: Decoy\n        run: SYNTHETIC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, stockFixtureWorkflowError([]byte(tt.source)))
		})
	}
}

// Named-lock checks independently inspect actual workflow selection, step env,
// evaluation and native source declarations; none proves hosted execution.
func namedLockWorkflowError(source []byte, root string) error {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Run  string
				Env  map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(source, &wf); err != nil {
		return fmt.Errorf("named workflow parse: %w", err)
	}
	run, evaluation := "", ""
	matches := 0
	for _, step := range wf.Jobs["windows-runtime"].Steps {
		if step.Name == "Run required native named-lock tests" {
			matches++
			run = step.Run
			if !reflect.DeepEqual(step.Env, map[string]string{"MTIX_WINDOWS_CAPTURE_DIR": "${{ runner.temp }}/mtix-windows-runtime"}) {
				return errors.New("named step requires exact capture env")
			}
		}
		if step.Name == stockNativeTestStepName {
			evaluation = step.Run
		}
	}
	if matches != 1 {
		return errors.New("one named native step required")
	}
	selected, err := namedLockCommands(run)
	if err != nil {
		return err
	}
	manifests, err := namedLockManifests(evaluation)
	if err != nil {
		return err
	}
	for directory, names := range selected {
		if !reflect.DeepEqual(names, manifests[directory]) {
			return errors.New("native selection and PASS manifest differ")
		}
		if err := namedLockSourceNames(root, directory, names); err != nil {
			return err
		}
	}
	return namedLockEvaluation(run, evaluation)
}

func namedLockCommands(run string) (map[string][]string, error) {
	pattern := regexp.MustCompile(`(?m)^& go test (\./[A-Za-z0-9_/]+) -race -count=1 -timeout=8m -json -run '([^']+)' > "\$env:MTIX_WINDOWS_CAPTURE_DIR/([A-Za-z0-9_-]+\.json)"$`)
	matches := pattern.FindAllStringSubmatch(run, -1)
	if len(matches) != 2 || strings.Count(run, "& go test ") != 2 {
		return nil, errors.New("two executable bounded native commands required")
	}
	expected := map[string]string{"./internal/sync/pushlock": "named-locks.json", "./cmd/mtix": "dispatch-lock.json"}
	result := map[string][]string{}
	for _, match := range matches {
		if expected[match[1]] != match[3] || result[match[1]] != nil {
			return nil, errors.New("wrong or duplicate named package/capture")
		}
		names, err := namedLockLiteralNames(match[2])
		if err != nil {
			return nil, err
		}
		result[match[1]] = names
	}
	return result, nil
}

func namedLockLiteralNames(filter string) ([]string, error) {
	if !strings.HasPrefix(filter, "^(") || !strings.HasSuffix(filter, ")$") {
		if filter == "^TestDispatchLock_HeldByOther_DaemonIdlesNotExits$" {
			return []string{"TestDispatchLock_HeldByOther_DaemonIdlesNotExits"}, nil
		}
		return nil, errors.New("anchored literal native selector required")
	}
	names := strings.Split(strings.TrimSuffix(strings.TrimPrefix(filter, "^("), ")$"), "|")
	used := map[string]bool{}
	literal := regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
	for _, name := range names {
		if !literal.MatchString(name) || used[name] {
			return nil, errors.New("literal unique native names required")
		}
		used[name] = true
	}
	return names, nil
}

func namedLockManifests(evaluation string) (map[string][]string, error) {
	pattern := regexp.MustCompile(`(?m)^\$(namedLockRequired|dispatchLockRequired) = @\(([^\r\n()]*)\)$`)
	matches := pattern.FindAllStringSubmatch(evaluation, -1)
	if len(matches) != 2 {
		return nil, errors.New("named PASS manifests required")
	}
	result := map[string][]string{}
	for _, match := range matches {
		names := []string{}
		for _, part := range strings.Split(match[2], ",") {
			part = strings.TrimSpace(part)
			if !regexp.MustCompile(`^'Test[A-Za-z0-9_/]+'$`).MatchString(part) {
				return nil, errors.New("literal PASS manifest required")
			}
			names = append(names, strings.Trim(part, "'"))
		}
		directory := "./internal/sync/pushlock"
		if match[1] == "dispatchLockRequired" {
			directory = "./cmd/mtix"
			required := []string{"TestDispatchLock_HeldByOther_DaemonIdlesNotExits", "TestDispatchLock_HeldByOther_DaemonIdlesNotExits/daemon", "TestDispatchLock_HeldByOther_DaemonIdlesNotExits/syncdaemon"}
			if !reflect.DeepEqual(names, required) {
				return nil, errors.New("both daemon subcase PASS/no SKIP manifests required")
			}
			names = names[:1]
		} else {
			for _, minimum := range []string{"TestAcquireNamed_SymlinkedLockFile_Refused", "TestAcquireNamed_SymlinkedDataDir_Refused", "TestAcquire_PushLockName_Unchanged"} {
				found := 0
				for _, name := range names {
					if name == minimum {
						found++
					}
				}
				if found != 1 {
					return nil, errors.New("minimum named-lock PASS manifest required")
				}
			}
		}
		if result[directory] != nil {
			return nil, errors.New("duplicate named manifest")
		}
		result[directory] = names
	}
	return result, nil
}

func namedLockSourceNames(root, directory string, names []string) error {
	context := build.Default
	context.GOOS = "windows"
	context.GOARCH = "amd64"
	context.CgoEnabled = true
	context.BuildTags = []string{"race"}
	context.UseAllFiles = false
	entries, err := os.ReadDir(filepath.Join(root, directory))
	if err != nil {
		return fmt.Errorf("native source directory: %w", err)
	}
	available := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		included, err := context.MatchFile(filepath.Join(root, directory), entry.Name())
		if err != nil {
			return fmt.Errorf("native build match: %w", err)
		}
		if !included {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, directory, entry.Name()), nil, 0)
		if err != nil {
			return fmt.Errorf("native source parse: %w", err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && namedLockRunnableTest(fn, parsed) {
				available[fn.Name.Name] = true
			}
		}
	}
	for _, name := range names {
		if !available[name] {
			return fmt.Errorf("selected native test declaration unavailable: %s", name)
		}
	}
	return nil
}

func namedLockRunnableTest(fn *ast.FuncDecl, file *ast.File) bool {
	if !namedLockTestShape(fn) {
		return false
	}
	parameter := fn.Type.Params.List[0]
	if len(parameter.Names) != 1 {
		return false
	}
	pointer, ok := parameter.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selected, ok := pointer.X.(*ast.SelectorExpr)
	if !ok || selected.Sel.Name != "T" {
		return false
	}
	qualifier, ok := selected.X.(*ast.Ident)
	if !ok {
		return false
	}
	for _, imported := range file.Imports {
		if imported.Path.Value == `"testing"` {
			name := "testing"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			return qualifier.Name == name
		}
	}
	return false
}

func namedLockEvaluation(run, evaluation string) error {
	requiredRun := []string{"$namedLockExit = $LASTEXITCODE", "$dispatchLockExit = $LASTEXITCODE", `if ($namedLockExit -ne 0 -or $dispatchLockExit -ne 0) { throw 'Named-lock native test failure' }`, `$env:MTIX_SYNC_DSN = ''`, `$env:MTIX_PG_TEST_DSN = ''`}
	requiredEvaluation := []string{
		`foreach ($namedSuite in @(@{ file='named-locks.json'; package='github.com/hyper-swe/mtix/internal/sync/pushlock'; tests=$namedLockRequired }, @{ file='dispatch-lock.json'; package='github.com/hyper-swe/mtix/cmd/mtix'; tests=$dispatchLockRequired })) {`,
		`$nativeEvents = Get-Content -LiteralPath "$env:MTIX_WINDOWS_CAPTURE_DIR/$($namedSuite.file)" | ForEach-Object { $_ | ConvertFrom-Json }`,
		`$nativePassed = @($nativeEvents | Where-Object { $_.Package -eq $namedSuite.package -and $_.Test -eq $nativeTest -and $_.Action -eq 'pass' })`,
		`$nativeSkipped = @($nativeEvents | Where-Object { $_.Package -eq $namedSuite.package -and $_.Test -eq $nativeTest -and $_.Action -eq 'skip' })`,
		`if ($nativePassed.Count -ne 1 -or $nativeSkipped.Count -ne 0) { throw "Required named-lock native test did not PASS: $nativeTest" }`,
		`$nativePackagePassed = @($nativeEvents | Where-Object { $_.Package -eq $namedSuite.package -and !$_.Test -and $_.Action -eq 'pass' })`,
		`if ($nativePackagePassed.Count -ne 1) { throw 'Named-lock native package did not PASS' }`,
		`if ($null -eq $nativeExits.named_lock -or $null -eq $nativeExits.dispatch_lock -or $nativeExits.named_lock -ne 0 -or $nativeExits.dispatch_lock -ne 0) { throw 'Named-lock native exits missing or failed' }`,
	}
	for _, pair := range []struct {
		script string
		lines  []string
	}{{run, requiredRun}, {evaluation, requiredEvaluation}} {
		for _, expected := range pair.lines {
			count := 0
			for _, line := range strings.Split(pair.script, "\n") {
				if strings.TrimSpace(line) == expected {
					count++
				}
			}
			if count != 1 {
				return errors.New("exact executable native result/exit evaluation required")
			}
		}
	}
	return nil
}

func TestWindowsNamedLockWorkflow_IndependentHostileControls(t *testing.T) {
	source, err := os.ReadFile("../../.github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	cases := []struct{ name, old, replacement string }{
		{"selector omitted", "^TestDispatchLock_HeldByOther_DaemonIdlesNotExits$", "^TestAbsent$"},
		{"selector broadened", "^TestDispatchLock_HeldByOther_DaemonIdlesNotExits$", "^TestDispatchLock_.*$"},
		{"list no-op", "-json -run '^TestDispatchLock", "-list '^TestDispatchLock"},
		{"missing public subcase", "'TestDispatchLock_HeldByOther_DaemonIdlesNotExits/syncdaemon'", "'TestAbsent/syncdaemon'"},
		{"ignored exit", "$namedLockExit = $LASTEXITCODE", "$namedLockExit = 0"},
		{"ignored PASS", "$nativePassed.Count -ne 1", "$false"},
		{"accepted SKIP", "$nativeSkipped.Count -ne 0", "$false"},
		{"wrong package", "tests=$namedLockRequired", "tests=$dispatchLockRequired"},
		{"unavailable declaration", "TestNamedLock_WindowsMetadata_ValidatesTypeAndOwner", "TestUnavailableNativeDeclaration"},
		{"unavailable owner-policy declaration", "TestNamedLock_WindowsOwnerPolicy_AcceptsUserAndElevatedAdministrators", "TestUnavailableNativeDeclaration"},
		{"unavailable real-owner declaration", "TestNamedLock_WindowsOwner_RealObjectsOwnedByUser", "TestUnavailableNativeDeclaration"},
		{"unavailable admin-gate declaration", "TestNamedLock_WindowsAdminGate_ElevationAndOwnerFlags", "TestUnavailableNativeDeclaration"},
		{"unavailable lock-file-access declaration", "TestNamedLock_WindowsLockFile_UserOnlyAccessAndNoDelete", "TestUnavailableNativeDeclaration"},
		{"unavailable legacy-directory declaration", "TestNamedLock_WindowsLegacyDirectoryAndForeignDirectory", "TestUnavailableNativeDeclaration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, string(source), tc.old)
			mutant := strings.ReplaceAll(string(source), tc.old, tc.replacement)
			require.Error(t, namedLockWorkflowError([]byte(mutant), "../.."))
		})
	}
	t.Run("new step wrong env", func(t *testing.T) {
		marker := "- name: Run required native named-lock tests\n        env:\n          MTIX_WINDOWS_CAPTURE_DIR: ${{ runner.temp }}/mtix-windows-runtime"
		require.Contains(t, string(source), marker)
		require.Error(t, namedLockWorkflowError([]byte(strings.Replace(string(source), marker, strings.Replace(marker, "runner.temp", "github.workspace", 1), 1)), "../.."))
	})
}

func namedLockTestShape(fn *ast.FuncDecl) bool {
	return fn.Recv == nil && fn.Body != nil && len(fn.Body.List) > 0 && fn.Type.Results == nil && fn.Type.TypeParams == nil && fn.Type.Params != nil && len(fn.Type.Params.List) == 1
}
