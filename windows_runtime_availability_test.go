// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Independently compare actual workflow manifests with Windows-buildable Go
// test declarations. This proves availability, never native execution or PASS.
package mtix_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type windowsAvailableSuite struct {
	directory   string
	packageName string
	filter      string
	required    []string
	optional    []string
}

type windowsNativeCommand struct{ directory, filter string }

func TestCI_WindowsRuntimeAvailability_ActualManifestBuildable(t *testing.T) {
	src, err := os.ReadFile(".github/workflows/windows-runtime.yml")
	require.NoError(t, err)
	require.NoError(t, windowsActualAvailabilityError(".", src))
}

func windowsActualAvailabilityError(root string, src []byte) error {
	run, err := windowsNativeScript(src)
	if err != nil {
		return err
	}
	suites, err := windowsLiteralSuites(root, run)
	if err != nil {
		return err
	}
	var result error
	for _, suite := range suites {
		names, sourceErr := windowsAvailableTests(filepath.Join(root, suite.directory))
		if sourceErr != nil {
			result = errors.Join(result, sourceErr)
			continue
		}
		result = errors.Join(result, windowsSuiteAvailabilityError(suite, names))
	}
	return result
}

func windowsNativeScript(src []byte) (string, error) {
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Run  string
			}
		}
	}
	if err := yaml.Unmarshal(src, &wf); err != nil {
		return "", fmt.Errorf("parse actual workflow: %w", err)
	}
	var runs []string
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if step.Name == "Run required native ACL and hooks status tests" {
				runs = append(runs, step.Run)
			}
		}
	}
	if len(runs) != 1 || runs[0] == "" {
		return "", errors.New("actual workflow needs one executable native-test step")
	}
	return runs[0], nil
}

func windowsLiteralSuites(root, run string) ([]windowsAvailableSuite, error) {
	commands, err := windowsLiteralCommands(run)
	if err != nil {
		return nil, err
	}
	arrays, err := windowsLiteralArrays(run)
	if err != nil {
		return nil, err
	}
	module, err := windowsSourceModule(root)
	if err != nil {
		return nil, err
	}
	binding := regexp.MustCompile(`@\{ file='([^']+)'; package='([^']+)'; names=\$(required[A-Za-z]+) \}`)
	matches := binding.FindAllStringSubmatch(run, -1)
	if len(matches) != len(commands) || len(matches) != len(arrays) {
		return nil, errors.New("native package/manifest bindings must be complete")
	}
	suites := []windowsAvailableSuite{}
	used := map[string]bool{}
	usedManifests := map[string]bool{}
	for _, match := range matches {
		command, exists := commands[match[1]]
		if !exists || used[match[1]] || usedManifests[match[3]] || arrays[match[3]] == nil {
			return nil, errors.New("duplicate or unknown native package/manifest binding")
		}
		expectedPackage := module + "/" + strings.TrimPrefix(command.directory, "./")
		if match[2] != expectedPackage {
			return nil, fmt.Errorf("native binding %s differs from source package %s", match[2], expectedPackage)
		}
		used[match[1]] = true
		usedManifests[match[3]] = true
		suites = append(suites, windowsAvailableSuite{directory: command.directory, packageName: match[2], filter: command.filter, required: arrays[match[3]]})
	}
	return windowsLiteralOptional(run, suites)
}

func windowsLiteralCommands(run string) (map[string]windowsNativeCommand, error) {
	pattern := regexp.MustCompile(`(?m)^& go test (\./[A-Za-z0-9_/]+) -race -count=1 -timeout=8m -json -run '([^']+)' > "\$env:MTIX_WINDOWS_CAPTURE_DIR/([A-Za-z0-9_-]+\.json)"$`)
	matches := pattern.FindAllStringSubmatch(run, -1)
	if len(matches) == 0 || len(matches) != strings.Count(run, "& go test ") {
		return nil, errors.New("native invocation must use the reviewed literal package/filter form")
	}
	commands := map[string]windowsNativeCommand{}
	directories := map[string]bool{}
	for _, match := range matches {
		if commands[match[3]].directory != "" || directories[match[1]] {
			return nil, errors.New("duplicate native invocation")
		}
		if _, err := regexp.Compile(match[2]); err != nil {
			return nil, fmt.Errorf("parse native test filter: %w", err)
		}
		commands[match[3]] = windowsNativeCommand{match[1], match[2]}
		directories[match[1]] = true
	}
	return commands, nil
}

func windowsLiteralArrays(run string) (map[string][]string, error) {
	pattern := regexp.MustCompile(`(?m)^\$(required[A-Za-z]+) = @\(([^\r\n()]*)\)$`)
	matches := pattern.FindAllStringSubmatch(run, -1)
	arrays := map[string][]string{}
	for _, match := range matches {
		if arrays[match[1]] != nil {
			return nil, errors.New("duplicate literal required manifest")
		}
		names, err := windowsLiteralNames(match[2])
		if err != nil {
			return nil, err
		}
		arrays[match[1]] = names
	}
	if len(arrays) == 0 {
		return nil, errors.New("actual native required manifests missing")
	}
	return arrays, nil
}

func windowsLiteralNames(text string) ([]string, error) {
	parts := strings.Split(text, ",")
	pattern := regexp.MustCompile(`^'Test[A-Za-z0-9_]*'$`)
	names := []string{}
	used := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if !pattern.MatchString(part) {
			return nil, errors.New("native required manifest must contain nonempty literal test names")
		}
		name := strings.Trim(part, "'")
		if used[name] {
			return nil, fmt.Errorf("duplicate required test %s", name)
		}
		used[name] = true
		names = append(names, name)
	}
	return names, nil
}

func windowsLiteralOptional(run string, suites []windowsAvailableSuite) ([]windowsAvailableSuite, error) {
	pattern := regexp.MustCompile(`if \(\$package -eq '([^']+)'\) \{\s*\$optionalAlias = '(Test[A-Za-z0-9_]+)'`)
	matches := pattern.FindAllStringSubmatch(run, -1)
	if len(matches) != 1 || strings.Count(run, "$optionalAlias = ") != 1 {
		return nil, errors.New("actual optional alias binding must appear exactly once")
	}
	for i := range suites {
		if suites[i].packageName == matches[0][1] {
			suites[i].optional = []string{matches[0][2]}
			return suites, nil
		}
	}
	return nil, errors.New("optional alias bound to an unexecuted package")
}

func windowsSourceModule(root string) (string, error) {
	src, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read source module identity: %w", err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", errors.New("source module identity missing")
}

func windowsSuiteAvailabilityError(suite windowsAvailableSuite, available map[string]string) error {
	filter, err := regexp.Compile(suite.filter)
	if err != nil {
		return fmt.Errorf("compile selected filter: %w", err)
	}
	gated := map[string]bool{}
	var result error
	for _, name := range append(append([]string{}, suite.required...), suite.optional...) {
		gated[name] = true
		if _, exists := available[name]; !exists {
			result = errors.Join(result, fmt.Errorf("%s/%s unavailable under windows/amd64/CGO1/race", suite.packageName, name))
		}
		if !filter.MatchString(name) {
			result = errors.Join(result, fmt.Errorf("%s/%s excluded by actual invocation filter", suite.packageName, name))
		}
	}
	selected := 0
	for name := range available {
		if filter.MatchString(name) {
			selected++
			if !gated[name] {
				result = errors.Join(result, fmt.Errorf("%s/%s selected without a result gate", suite.packageName, name))
			}
		}
	}
	if selected == 0 {
		result = errors.Join(result, fmt.Errorf("%s has an empty native selection", suite.packageName))
	}
	return result
}
