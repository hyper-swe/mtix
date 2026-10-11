// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Portable captured-fixture checks prove bytes, JSON structure and provenance.
// They do not parse Windows SID/SDDL or establish native policy acceptance.
package oplocal

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const capturedStockFixturePath = "testdata/windows-stock-2026-10-11-38146429820.json"
const capturedStockFixtureSHA256 = "00c5ddba6676880c9750e03cd2f421578aab15ae891c79529bac240baa4d6fe7"

type stockFixtureIdentity struct {
	Schema         int    `json:"schema"`
	HelperVersion  string `json:"helper_version"`
	CapturedUTC    string `json:"captured_utc"`
	GoVersion      string `json:"go_version"`
	ImageOS        string `json:"image_os"`
	ImageVersion   string `json:"image_version"`
	WindowsVersion string `json:"windows_version"`
	WindowsBuild   uint32 `json:"windows_build"`
	UserSID        string `json:"user_sid"`
	HeadSHA        string `json:"head_sha"`
	CheckoutSHA    string `json:"checkout_sha"`
	Ref            string `json:"ref"`
	RunID          string `json:"run_id"`
	RunAttempt     string `json:"run_attempt"`
	RunURL         string `json:"run_url"`
}

type capturedStockFixture struct {
	stockFixtureIdentity
	IdentityError   *string           `json:"identity_error,omitempty"`
	ProvenanceError *string           `json:"provenance_error,omitempty"`
	Rows            []stockFixtureRow `json:"rows"`
}

type stockFixtureRow struct {
	LogicalPath    string  `json:"logical_path"`
	SDDL           string  `json:"sddl"`
	PolicyAccepted bool    `json:"policy_accepted"`
	ReplayAccepted bool    `json:"replay_accepted"`
	ReadError      *string `json:"read_error,omitempty"`
	ParseError     *string `json:"parse_error,omitempty"`
	PolicyError    *string `json:"policy_error,omitempty"`
	ReplayError    *string `json:"replay_error,omitempty"`
	CloseError     *string `json:"close_error,omitempty"`
}

func capturedStockIdentity() stockFixtureIdentity {
	return stockFixtureIdentity{
		Schema: 1, HelperVersion: "stock-acl-v1", CapturedUTC: "2026-10-11T14:16:05.2992491Z",
		GoVersion: "go1.26.9", ImageOS: "win25-vs2026", ImageVersion: "20260925.250.1",
		WindowsVersion: "10.0", WindowsBuild: 26100, UserSID: "S-1-5-21-1643835476-1616584234-1346609752-500",
		HeadSHA: "a52856a10b40cc09fc64daf4882e2ef7471f66ed", CheckoutSHA: "c93d26eb8cbb3f8076b0fc01ec8f3ec1e7e825a1",
		Ref: "refs/pull/78/merge", RunID: "38146429820", RunAttempt: "1",
		RunURL: "https://github.com/hyper-swe/mtix/actions/runs/38146429820",
	}
}

func loadCapturedStockFixture() (capturedStockFixture, error) {
	data, err := os.ReadFile(capturedStockFixturePath)
	if err != nil {
		return capturedStockFixture{}, fmt.Errorf("read genuine captured stock fixture: %w", err)
	}
	return parseStockFixture(data, capturedStockFixtureSHA256)
}

func parseStockFixture(data []byte, expectedSHA256 string) (capturedStockFixture, error) {
	var capture capturedStockFixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capture); err != nil {
		return capture, fmt.Errorf("decode stock fixture: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return capture, fmt.Errorf("stock fixture must contain exactly one JSON object: %v", err)
	}
	if capture.stockFixtureIdentity != capturedStockIdentity() {
		return capture, fmt.Errorf("stock fixture identity/provenance differs from native run 38146429820 attempt 1")
	}
	if capture.IdentityError != nil || capture.ProvenanceError != nil {
		return capture, fmt.Errorf("stock fixture contains identity/provenance error fields")
	}
	if err := stockFixtureRowsError(capture.Rows); err != nil {
		return capture, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != expectedSHA256 {
		return capture, fmt.Errorf("stock fixture SHA-256 differs from required original bytes")
	}
	return capture, nil
}

func stockFixtureRowsError(rows []stockFixtureRow) error {
	labels := []string{"volume_root", "users_root", "runner_profile"}
	if len(rows) != len(labels) {
		return fmt.Errorf("stock fixture must contain exactly three ordered logical paths")
	}
	for i, row := range rows {
		if row.LogicalPath != labels[i] {
			return fmt.Errorf("stock fixture row %d has incorrect logical path %q", i, row.LogicalPath)
		}
		if strings.TrimSpace(row.SDDL) == "" {
			return fmt.Errorf("stock fixture row %s has empty raw SDDL", row.LogicalPath)
		}
		if !row.PolicyAccepted || !row.ReplayAccepted {
			return fmt.Errorf("stock fixture row %s was not policy/replay accepted", row.LogicalPath)
		}
		if row.ReadError != nil || row.ParseError != nil || row.PolicyError != nil || row.ReplayError != nil || row.CloseError != nil {
			return fmt.Errorf("stock fixture row %s contains an error field", row.LogicalPath)
		}
	}
	return nil
}

func TestStockFixture_CapturedNativeArtifact_RequiresExactIntegrity(t *testing.T) {
	_, err := loadCapturedStockFixture()
	require.NoError(t, err, "the exact genuine dated native artifact is required")
}

// SYNTHETIC: metadata uses pinned expectations to exercise the validator;
// descriptors are conspicuously non-native text, never genuine captured data.
func syntheticStockFixture() capturedStockFixture {
	capture := capturedStockFixture{stockFixtureIdentity: capturedStockIdentity()}
	for _, label := range []string{"volume_root", "users_root", "runner_profile"} {
		capture.Rows = append(capture.Rows, stockFixtureRow{
			LogicalPath: label, SDDL: "SYNTHETIC loader control, not Windows SDDL",
			PolicyAccepted: true, ReplayAccepted: true,
		})
	}
	return capture
}

func TestStockFixture_SYNTHETICStructureControls_RejectInvalidCapture(t *testing.T) {
	errorText := "SYNTHETIC injected capture failure"
	tests := []struct {
		name   string
		mutate func(*capturedStockFixture)
		want   string
	}{
		{"valid synthetic structure", func(*capturedStockFixture) {}, ""},
		{"missing schema", func(c *capturedStockFixture) { c.Schema = 0 }, "identity/provenance"},
		{"wrong helper", func(c *capturedStockFixture) { c.HelperVersion = "SYNTHETIC" }, "identity/provenance"},
		{"missing date", func(c *capturedStockFixture) { c.CapturedUTC = "" }, "identity/provenance"},
		{"wrong date", func(c *capturedStockFixture) { c.CapturedUTC = "2026-10-12T00:00:00Z" }, "identity/provenance"},
		{"wrong Go", func(c *capturedStockFixture) { c.GoVersion = "SYNTHETIC" }, "identity/provenance"},
		{"missing image OS", func(c *capturedStockFixture) { c.ImageOS = "" }, "identity/provenance"},
		{"wrong image", func(c *capturedStockFixture) { c.ImageVersion = "SYNTHETIC" }, "identity/provenance"},
		{"missing Windows version", func(c *capturedStockFixture) { c.WindowsVersion = "" }, "identity/provenance"},
		{"missing build", func(c *capturedStockFixture) { c.WindowsBuild = 0 }, "identity/provenance"},
		{"missing SID", func(c *capturedStockFixture) { c.UserSID = "" }, "identity/provenance"},
		{"wrong SID", func(c *capturedStockFixture) { c.UserSID = "S-1-5-21-1-2-3-500" }, "identity/provenance"},
		{"missing head", func(c *capturedStockFixture) { c.HeadSHA = "" }, "identity/provenance"},
		{"wrong checkout", func(c *capturedStockFixture) { c.CheckoutSHA = c.HeadSHA }, "identity/provenance"},
		{"wrong ref", func(c *capturedStockFixture) { c.Ref = "refs/heads/main" }, "identity/provenance"},
		{"wrong run", func(c *capturedStockFixture) { c.RunID = "0" }, "identity/provenance"},
		{"wrong attempt", func(c *capturedStockFixture) { c.RunAttempt = "2" }, "identity/provenance"},
		{"missing run URL", func(c *capturedStockFixture) { c.RunURL = "" }, "identity/provenance"},
		{"identity error", func(c *capturedStockFixture) { c.IdentityError = &errorText }, "error fields"},
		{"provenance error", func(c *capturedStockFixture) { c.ProvenanceError = &errorText }, "error fields"},
		{"missing rows", func(c *capturedStockFixture) { c.Rows = nil }, "three ordered"},
		{"missing row", func(c *capturedStockFixture) { c.Rows = c.Rows[:2] }, "three ordered"},
		{"extra row", func(c *capturedStockFixture) { c.Rows = append(c.Rows, c.Rows[0]) }, "three ordered"},
		{"duplicate label", func(c *capturedStockFixture) { c.Rows[1].LogicalPath = c.Rows[0].LogicalPath }, "logical path"},
		{"wrong label", func(c *capturedStockFixture) { c.Rows[2].LogicalPath = "SYNTHETIC" }, "logical path"},
		{"wrong order", func(c *capturedStockFixture) { c.Rows[0], c.Rows[1] = c.Rows[1], c.Rows[0] }, "logical path"},
		{"empty SDDL", func(c *capturedStockFixture) { c.Rows[0].SDDL = "" }, "empty raw SDDL"},
		{"blank SDDL", func(c *capturedStockFixture) { c.Rows[1].SDDL = " \n" }, "empty raw SDDL"},
		{"policy refused", func(c *capturedStockFixture) { c.Rows[0].PolicyAccepted = false }, "not policy/replay accepted"},
		{"replay refused", func(c *capturedStockFixture) { c.Rows[2].ReplayAccepted = false }, "not policy/replay accepted"},
		{"read error", func(c *capturedStockFixture) { c.Rows[0].ReadError = &errorText }, "error field"},
		{"parse error", func(c *capturedStockFixture) { c.Rows[1].ParseError = &errorText }, "error field"},
		{"policy error", func(c *capturedStockFixture) { c.Rows[2].PolicyError = &errorText }, "error field"},
		{"replay error", func(c *capturedStockFixture) { c.Rows[0].ReplayError = &errorText }, "error field"},
		{"close error", func(c *capturedStockFixture) { c.Rows[2].CloseError = &errorText }, "error field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := syntheticStockFixture()
			tt.mutate(&capture)
			data, err := json.Marshal(capture)
			require.NoError(t, err)
			_, err = parseStockFixture(data, fmt.Sprintf("%x", sha256.Sum256(data)))
			if tt.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
}

func TestStockFixture_SYNTHETICByteControls_RejectCorruptJSONOrHash(t *testing.T) {
	valid, err := json.Marshal(syntheticStockFixture())
	require.NoError(t, err)
	tests := []struct {
		name string
		data []byte
		sha  string
		want string
	}{
		{"empty", nil, "", "decode stock fixture"},
		{"malformed", []byte("{"), "", "decode stock fixture"},
		{"null", []byte("null"), "", "identity/provenance"},
		{"empty object", []byte("{}"), "", "identity/provenance"},
		{"unknown field", bytes.Replace(valid, []byte("\"schema\""), []byte("\"unknown_schema\""), 1), "", "unknown field"},
		{"trailing object", append(bytes.Clone(valid), []byte("{}")...), "", "exactly one"},
		{"trailing garbage", append(bytes.Clone(valid), '!'), "", "exactly one"},
		{"wrong hash", valid, strings.Repeat("0", 64), "SHA-256"},
		{"synthetic is not original", valid, capturedStockFixtureSHA256, "SHA-256"},
		{"raw whitespace change", append(bytes.Clone(valid), '\n'), fmt.Sprintf("%x", sha256.Sum256(valid)), "SHA-256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseStockFixture(tt.data, tt.sha)
			require.ErrorContains(t, err, tt.want)
		})
	}
}
