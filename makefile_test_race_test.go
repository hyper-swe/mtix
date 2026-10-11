// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local race-budget guards inspect the actual Make target and CI test-go steps.
package mtix_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMakefile_TestRaceTimeout_MatchesCI(t *testing.T) {
	makefile, err := os.ReadFile("Makefile")
	require.NoError(t, err)
	ci, err := os.ReadFile(".github/workflows/ci.yml")
	require.NoError(t, err)
	require.NoError(t, raceTimeoutError(makefile, ci))
}

func raceTimeoutError(makefile, ci []byte) error {
	recipe, err := makeRaceRecipe(makefile)
	if err != nil {
		return err
	}
	local, err := singleGoTestTimeout(recipe)
	if err != nil {
		return fmt.Errorf("test-race recipe: %w", err)
	}
	var workflow toolchainWorkflow
	if err := yaml.Unmarshal(ci, &workflow); err != nil {
		return fmt.Errorf("CI workflow: %w", err)
	}
	job, exists := workflow.Jobs["test-go"]
	if !exists {
		return fmt.Errorf("CI test-go job missing")
	}
	var commands []string
	for _, step := range job.Steps {
		commands = append(commands, strings.Split(step.Run, "\n")...)
	}
	hosted, err := singleGoTestTimeout(commands)
	if err != nil {
		return fmt.Errorf("CI test-go step: %w", err)
	}
	if local != hosted || local != 20*time.Minute {
		return fmt.Errorf("race timeout must match CI and equal 20m: local=%s CI=%s", local, hosted)
	}
	return nil
}

func makeRaceRecipe(src []byte) ([]string, error) {
	var recipe []string
	inside, count := false, 0
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, "\t") {
			target, _, found := strings.Cut(line, ":")
			inside = found && strings.TrimSpace(target) == "test-race"
			if inside {
				count++
			}
			continue
		}
		if inside {
			recipe = append(recipe, strings.TrimPrefix(trimmed, "@"))
		}
	}
	if count != 1 {
		return nil, fmt.Errorf("expected one test-race target, got %d", count)
	}
	return recipe, nil
}

func singleGoTestTimeout(lines []string) (time.Duration, error) {
	var command string
	count := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "go" && fields[1] == "test" {
			command = line
			count++
		}
	}
	if count != 1 {
		return 0, fmt.Errorf("expected one go test command, got %d", count)
	}
	return explicitTestTimeout(command)
}

// Accept literal arguments and the two shipped package-list expansions only.
// Unsupported compound or quoted command shapes fail instead of being interpreted.
func explicitTestTimeout(command string) (time.Duration, error) {
	command, _, _ = strings.Cut(command, "#")
	for _, expansion := range []string{"$(GO_PKGS)", "$(go list ./... | grep -v node_modules)"} {
		command = strings.ReplaceAll(command, expansion, "packages")
	}
	if strings.ContainsAny(command, "\"'`$;|&<>\\") {
		return 0, fmt.Errorf("unsupported go test command shape")
	}
	fields := strings.Fields(command)
	var value string
	count := 0
	for i := 2; i < len(fields); i++ {
		if fields[i] == "--" {
			return 0, fmt.Errorf("timeout must be a go test flag")
		}
		if fields[i] == "-timeout" {
			count++
			i++
			if i == len(fields) {
				return 0, fmt.Errorf("timeout value missing")
			}
			value = fields[i]
		} else if strings.HasPrefix(fields[i], "-timeout=") {
			count++
			value = strings.TrimPrefix(fields[i], "-timeout=")
		}
	}
	if count != 1 {
		return 0, fmt.Errorf("expected one explicit timeout, got %d", count)
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid timeout duration %q", value)
	}
	return duration, nil
}

func TestMakefile_TestRaceTimeout_DriftAndRemovalRejected(t *testing.T) {
	makefile := "test-race:\n\tgo test $(GO_PKGS) -race -count=1 -timeout 20m\n"
	ci := "jobs:\n  test-go:\n    steps:\n      - run: go test $(go list ./... | grep -v node_modules) -race -count=1 -timeout 20m\n"
	cases := []struct{ name, makefile, ci string }{
		{"local drift", strings.Replace(makefile, "20m", "19m", 1), ci},
		{"CI drift", makefile, strings.Replace(ci, "20m", "19m", 1)},
		{"both drift", strings.Replace(makefile, "20m", "10m", 1), strings.Replace(ci, "20m", "10m", 1)},
		{"local missing", strings.Replace(makefile, " -timeout 20m", "", 1), ci},
		{"CI missing", makefile, strings.Replace(ci, " -timeout 20m", "", 1)},
		{"duplicate timeout", strings.Replace(makefile, "20m", "20m -timeout=20m", 1), ci},
		{"invalid timeout", strings.Replace(makefile, "20m", "later", 1), ci},
		{"argument separator", strings.Replace(makefile, "-timeout 20m", "-- -timeout 20m", 1), ci},
		{"no value", strings.Replace(makefile, " 20m", "", 1), ci},
		{"no target", strings.Replace(makefile, "test-race:", "test-cover:", 1), ci},
		{"duplicate target", makefile + makefile, ci},
		{"no CI job", makefile, strings.Replace(ci, "test-go:", "test-go-postgres-docker:", 1)},
		{"duplicate command", makefile, ci + "      - run: go test ./... -timeout 20m\n"},
		{"comment decoy", "test-race:\n\tgo test ./... # -timeout 20m\n", ci},
		{"substitution decoy", "test-race:\n\tgo test $(printf ' -timeout 20m')\n", ci},
		{"quoted decoy", "test-race:\n\techo 'go test ./... -timeout 20m'\n", ci},
		{"malformed YAML", makefile, "jobs: ["},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, raceTimeoutError([]byte(tc.makefile), []byte(tc.ci)))
		})
	}
}

func TestMakefile_TestRaceTimeout_RealBlocksIgnoreDecoys(t *testing.T) {
	makefile := ".PHONY: test-race\n# test-race: go test -timeout 1m\ntest-cover:\n\tgo test ./... -timeout 2m\ntest-race:\n\t# go test ./... -timeout 3m\n\tgo test $(GO_PKGS) -race -count=1 -timeout=20m # -timeout 4m\nhelp:\n\techo 'test-race: -timeout 5m'\n"
	ci := "jobs:\n  test-go:\n    steps:\n      - run: |\n          # go test ./... -timeout 6m\n          go test $(go list ./... | grep -v node_modules) -race -count=1 -timeout 20m\n  test-go-postgres-docker:\n    steps:\n      - run: go test ./... -timeout 7m\n  test-fault-injection:\n    steps:\n      - run: go test ./e2e/faultinject -timeout 12m\n"
	require.NoError(t, raceTimeoutError([]byte(makefile), []byte(ci)))
}
