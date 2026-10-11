// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Captured fixture checkout guards exercise real Git conversion in isolated
// temporary repositories. They preserve artifact bytes, not native ACL proof.
package mtix_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const stockCheckoutFixture = "internal/oplocal/testdata/windows-stock-2026-10-11-38146429820.json"
const stockCheckoutSHA = "00c5ddba6676880c9750e03cd2f421578aab15ae891c79529bac240baa4d6fe7"

type stockCheckoutResult struct {
	textAttribute string
	fixture       []byte
	unrelated     []byte
}

func stockCheckoutActualInput(t *testing.T) ([]byte, []byte) {
	t.Helper()
	fixture, err := os.ReadFile(stockCheckoutFixture)
	require.NoError(t, err)
	require.Len(t, fixture, 1669)
	require.Equal(t, stockCheckoutSHA, fmt.Sprintf("%x", sha256.Sum256(fixture)))
	attributes, err := os.ReadFile(".gitattributes")
	if os.IsNotExist(err) {
		return fixture, nil
	}
	require.NoError(t, err)
	return fixture, attributes
}

func stockCheckoutGitEnvironment(t *testing.T, directory string) []string {
	t.Helper()
	emptyConfig := filepath.Join(directory, "empty-config")
	require.NoError(t, os.WriteFile(emptyConfig, nil, 0o600))
	emptyTemplate := filepath.Join(directory, "empty-template")
	require.NoError(t, os.Mkdir(emptyTemplate, 0o700))
	environment := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			environment = append(environment, value)
		}
	}
	return append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+emptyConfig, "GIT_CONFIG_SYSTEM="+emptyConfig,
		"GIT_TEMPLATE_DIR="+emptyTemplate)
}

func stockCheckoutGit(t *testing.T, directory string, environment []string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	require.NoError(t, err, "isolated git %v: %s", args, output)
	return output
}

func stockCheckoutObserve(t *testing.T, fixture, attributes []byte) stockCheckoutResult {
	t.Helper()
	directory := t.TempDir()
	environment := stockCheckoutGitEnvironment(t, directory)
	git := func(args ...string) []byte { return stockCheckoutGit(t, directory, environment, args...) }
	git("init", "--quiet")
	git("config", "core.attributesFile", filepath.Join(directory, "empty-config"))
	git("config", "core.autocrlf", "false")
	git("config", "core.safecrlf", "false")
	git("config", "core.eol", "crlf")
	require.NoError(t, os.MkdirAll(filepath.Join(directory, filepath.Dir(stockCheckoutFixture)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, stockCheckoutFixture), fixture, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, ".gitattributes"), attributes, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "unrelated.txt"), []byte("SYNTHETIC unrelated text\nsecond line\n"), 0o600))
	git("add", "--", ".gitattributes", stockCheckoutFixture, "unrelated.txt")
	require.Equal(t, fixture, git("show", ":"+stockCheckoutFixture), "Git index must retain original raw fixture")
	attribute := string(git("check-attr", "text", "--", stockCheckoutFixture))
	checkout := filepath.Join(directory, "checkout")
	require.NoError(t, os.Mkdir(checkout, 0o700))
	git("-c", "core.autocrlf=true", "checkout-index", "--prefix="+filepath.ToSlash(checkout)+"/", "--all")
	checked, err := os.ReadFile(filepath.Join(checkout, stockCheckoutFixture))
	require.NoError(t, err)
	unrelated, err := os.ReadFile(filepath.Join(checkout, "unrelated.txt"))
	require.NoError(t, err)
	return stockCheckoutResult{attribute, checked, unrelated}
}

func TestCI_WindowsStockFixture_ActualGitAttribute_DisablesTextConversion(t *testing.T) {
	fixture, attributes := stockCheckoutActualInput(t)
	result := stockCheckoutObserve(t, fixture, attributes)
	require.Equal(t, stockCheckoutFixture+": text: unset\n", result.textAttribute,
		"actual captured fixture must have Git text explicitly unset")
}

func TestCI_WindowsStockFixture_ActualGitCheckout_PreservesExactArtifact(t *testing.T) {
	fixture, attributes := stockCheckoutActualInput(t)
	result := stockCheckoutObserve(t, fixture, attributes)
	require.Equal(t, fixture, result.fixture, "actual Git checkout must preserve original captured bytes")
	require.Equal(t, stockCheckoutSHA, fmt.Sprintf("%x", sha256.Sum256(result.fixture)))
	require.Equal(t, []byte("SYNTHETIC unrelated text\r\nsecond line\r\n"), result.unrelated,
		"fixture protection must not disable conversion of unrelated text")
}

func TestCI_WindowsStockFixture_SYNTHETICGitAttributeControls_DistinguishByteAndScopeRules(t *testing.T) {
	fixture, _ := stockCheckoutActualInput(t)
	cases := []struct {
		name, attributes, text        string
		preserved, unrelatedConverted bool
	}{
		{"exact path minus text", stockCheckoutFixture + " -text\n", "unset", true, true},
		{"exact path binary", stockCheckoutFixture + " binary\n", "unset", true, true},
		{"missing rule", "", "unspecified", false, true},
		{"unrelated path", "other.json -text\n", "unspecified", false, true},
		{"text conversion", stockCheckoutFixture + " text\n", "set", false, true},
		{"explicit CRLF", stockCheckoutFixture + " text eol=crlf\n", "set", false, true},
		{"LF preserves bytes but does not unset text", stockCheckoutFixture + " text eol=lf\n", "set", true, true},
		{"wildcard protects unrelated text too", "* -text\n", "unset", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := stockCheckoutObserve(t, fixture, []byte(tc.attributes))
			require.Equal(t, stockCheckoutFixture+": text: "+tc.text+"\n", result.textAttribute)
			require.Equal(t, tc.preserved, bytes.Equal(fixture, result.fixture))
			require.Equal(t, tc.unrelatedConverted, bytes.Equal([]byte("SYNTHETIC unrelated text\r\nsecond line\r\n"), result.unrelated))
			if !tc.preserved {
				require.Equal(t, bytes.ReplaceAll(fixture, []byte("\n"), []byte("\r\n")), result.fixture)
			}
		})
	}
}
