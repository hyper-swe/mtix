// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// Decision reports one exec hook's approval and corrective command (FR-19).
type Decision struct {
	Stanza  string `json:"-"`
	Name    string `json:"name"`
	Trusted bool   `json:"trusted"`
	Detail  string `json:"detail"`
	Fix     string `json:"fix"`
}

// Approvals accesses host-specific per-hook records (FR-19).
type Approvals struct {
	dir     string
	state   *oplocal.State
	session bool
	// readLegacy reads the legacy workspace record; injectable for tests.
	readLegacy func(string) string
	// clock supplies the current time for the migration window (FR-19).
	clock func() time.Time
}
type approvalRecord struct {
	Project string `json:"project"`
	Hook    string `json:"hook"`
	Digest  string `json:"digest"`
}
type migrationRecord struct {
	Project   string            `json:"project"`
	Attempted bool              `json:"attempted"`
	Pins      map[string]string `json:"pins"`
	Detail    string            `json:"detail"`
}

// NewApprovals uses explicit operator state and process eligibility inputs.
func NewApprovals(dir string, state *oplocal.State, session bool) *Approvals {
	return &Approvals{dir: dir, state: state, session: session, readLegacy: TrustedHash, clock: time.Now}
}

// SessionMarked reports process markers that defer operator approval (FR-19).
func SessionMarked() bool {
	for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CLAUDECODE", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_ENV_FILE", "PI_SESSION_ID", "PI_SESSION_FILE", "PI_CODING_AGENT"} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// ProcessApprovals resolves state from the operator process, without creating it.
func ProcessApprovals(dir string) (*Approvals, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, fmt.Errorf("operator inputs: %w", err)
	}
	return NewApprovals(dir, oplocal.New(env), SessionMarked()), nil
}

func recordKey(kind, project, hook string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + hook))
	return kind + hex.EncodeToString(sum[:])[:64-len(kind)]
}

// TrustFix returns a shell-quoted command for one hook.
func TrustFix(name string) string {
	return "mtix hooks trust -- '" + strings.ReplaceAll(name, "'", "'\"'\"'") + "'"
}

// Approve writes exactly one configured exec hook after validating grant inputs.
func (a *Approvals) Approve(name string, terminal bool) error {
	return a.approve(name, "", terminal)
}

func (a *Approvals) approve(name, expected string, terminal bool) error {
	if !terminal || a.session {
		return fmt.Errorf("hook approval requires terminal stdin outside an agent session")
	}
	snap, err := approvalSnapshot(a.dir)
	if err != nil {
		return err
	}
	if hookErr := snap.errors[name]; hookErr != nil {
		return hookErr
	}
	if expected != "" && snap.stanzas[name] != expected {
		return fmt.Errorf("selected hook configuration differs; review the current hook")
	}
	digest, ok := snap.digests[name]
	if !ok {
		return fmt.Errorf("configured exec hook %q not found", name)
	}
	project, err := projectIdentity(a.dir)
	if err != nil {
		return err
	}
	rec := approvalRecord{Project: project, Hook: name, Digest: digest}
	if err := a.state.WriteJSON(recordKey("hook-", project, name), rec); err != nil {
		return fmt.Errorf("save hook approval: %w", err)
	}
	return nil
}

// Check reports hook decisions without creating state or migrating records.
func (a *Approvals) Check() (map[string]Decision, error) {
	cfg, _ := Load(a.dir)
	out := map[string]Decision{}
	for _, h := range cfg.Hooks {
		if h.Exec != nil && slices.Contains(h.Deliver, AdapterExec) {
			out[h.Name] = Decision{Name: h.Name, Detail: "approval required", Fix: TrustFix(h.Name)}
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	snap, err := approvalSnapshot(a.dir)
	if err != nil {
		return out, err
	}
	project, err := projectIdentity(a.dir)
	if err != nil {
		return out, err
	}
	var reports error
	for name, decision := range out {
		if hookErr := snap.errors[name]; hookErr != nil {
			decision.Detail = hookErr.Error()
			out[name] = decision
			reports = errors.Join(reports, hookErr)
			continue
		}
		decision.Stanza = snap.stanzas[name]
		decision.Trusted, decision.Detail, err = a.readDecision(project, name, snap.digests[name])
		out[name] = decision
		reports = errors.Join(reports, err)
	}
	return out, reports
}

func (a *Approvals) readDecision(project, name, digest string) (bool, string, error) {
	var rec approvalRecord
	present, reports, err := readApprovalRecord(a.state, recordKey("hook-", project, name), &rec)
	if err != nil {
		return false, "operator state refused", err
	}
	if len(reports) != 0 {
		return false, reports[0].Reason, nil
	}
	if present {
		if rec.Project != project || rec.Hook != name || !validDigest(rec.Digest) {
			return false, "approval record is invalid", fmt.Errorf("hook %q approval record is invalid", name)
		}
		if rec.Digest != digest {
			return false, "hook or project script differs", nil
		}
		return true, "per-hook approval", nil
	}
	return a.readMigrationDecision(project, name, digest)
}

func (a *Approvals) readMigrationDecision(project, name, digest string) (bool, string, error) {
	var migration migrationRecord
	present, reports, err := readApprovalRecord(a.state, recordKey("migration-", project, ""), &migration)
	if err != nil {
		return false, "operator state refused", err
	}
	if len(reports) != 0 {
		return false, reports[0].Reason, nil
	}
	if !present {
		return false, "approval required", nil
	}
	if migration.Project != project || !migration.Attempted {
		return false, "migration record is invalid", fmt.Errorf("migration record is invalid")
	}
	pin := migration.Pins[name]
	if pin != "" && !validDigest(pin) {
		return false, "migration record is invalid", fmt.Errorf("migration digest is invalid")
	}
	return pin != "" && pin == digest, migration.Detail, nil
}

func validDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && strings.ToLower(value) == value
}

// Migrate records the first eligible legacy approval decision (FR-19).
func (a *Approvals) Migrate() error {
	cfg, _ := Load(a.dir)
	if a.session || !hasExec(cfg) {
		return nil
	}
	project, err := projectIdentity(a.dir)
	if err != nil {
		return err
	}
	return withApprovalLock(a.state, project, func() error { return a.migrateLocked(project) })
}

func hasExec(cfg Config) bool {
	for _, h := range cfg.Hooks {
		if h.Exec != nil && slices.Contains(h.Deliver, AdapterExec) {
			return true
		}
	}
	return false
}

func (a *Approvals) migrateLocked(project string) error {
	key := recordKey("migration-", project, "")
	var existing migrationRecord
	present, reports, err := readApprovalRecord(a.state, key, &existing)
	if err != nil {
		return err
	}
	if len(reports) != 0 {
		return fmt.Errorf("migration record: %s", reports[0].Reason)
	}
	if present {
		if existing.Project != project || !existing.Attempted {
			return fmt.Errorf("migration record is invalid")
		}
		return nil
	}
	rec := migrationRecord{Project: project, Attempted: true, Pins: map[string]string{}, Detail: "legacy approval not migrated"}
	tracked, reason := legacyTracked(filepath.Dir(a.dir))
	rec.Detail = reason
	if !tracked {
		a.applyLegacyPin(&rec)
	}
	if err := a.state.WriteJSON(key, rec); err != nil {
		return fmt.Errorf("save migration decision: %w", err)
	}
	return nil
}

func projectIdentity(dir string) (string, error) {
	root, err := filepath.Abs(filepath.Dir(dir))
	if err != nil {
		return "", fmt.Errorf("project path: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("project location: %w", err)
	}
	identity, err := directoryIdentity(root)
	if err != nil {
		return "", err
	}
	return root + "\x00" + identity, nil
}

// LegacyStatus reports an existing workspace record without using it as approval.
func LegacyStatus(dir string) string {
	_, err := os.Lstat(filepath.Join(dir, trustFileName))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		return "legacy workspace record cannot be inspected: " + err.Error()
	}
	return "legacy workspace record is ignored; inspect individual hook decisions"
}

func marshalHook(h Hook) ([]byte, error) { return json.Marshal(h) }

func readApprovalRecord(state *oplocal.State, name string, target any) (bool, []oplocal.Report, error) {
	var raw json.RawMessage
	reports, err := state.ReadJSON(name, &raw)
	if err != nil || len(reports) != 0 || raw == nil {
		return false, reports, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true, nil, fmt.Errorf("approval record is null")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return true, nil, fmt.Errorf("approval record data: %w", err)
	}
	return true, nil, nil
}

// CheckConfig validates decisions against the selected dispatch stanzas (FR-19).
func (a *Approvals) CheckConfig(cfg Config) (map[string]Decision, error) {
	decisions, reportErr := a.Check()
	for _, hook := range cfg.Hooks {
		decision := decisions[hook.Name]
		stanza, err := marshalHook(hook)
		if err != nil {
			return decisions, err
		}
		if decision.Stanza != string(stanza) {
			decision.Trusted = false
			decision.Detail = "hook configuration differs"
		}
		decisions[hook.Name] = decision
	}
	return decisions, reportErr
}

// ApproveSelected validates the operator's selected hook before writing (FR-19).
func (a *Approvals) ApproveSelected(selected Hook, terminal bool) error {
	stanza, err := marshalHook(selected)
	if err != nil {
		return fmt.Errorf("selected hook: %w", err)
	}
	return a.approve(selected.Name, string(stanza), terminal)
}

const firstRunKey = "migration-first-run"

type firstRunRecord struct {
	At time.Time `json:"at"`
}

// beforeFirstRun records the first migration time on this host and reports
// whether the legacy record predates it (FR-19).
func (a *Approvals) beforeFirstRun() bool {
	var first firstRunRecord
	present, reports, err := readApprovalRecord(a.state, firstRunKey, &first)
	if err != nil || len(reports) != 0 {
		return false
	}
	if !present || first.At.IsZero() {
		first.At = a.clock().UTC()
		if writeErr := a.state.WriteJSON(firstRunKey, first); writeErr != nil {
			return false
		}
	}
	info, err := os.Lstat(filepath.Join(a.dir, trustFileName))
	return err == nil && !info.ModTime().After(first.At)
}

// applyLegacyPin converts an eligible untracked legacy record into pins (FR-19).
func (a *Approvals) applyLegacyPin(rec *migrationRecord) {
	pin := a.readLegacy(a.dir)
	if !a.beforeFirstRun() && pin != "" {
		rec.Detail = "legacy workspace record is newer than the first run on this host"
		return
	}
	if !validDigest(pin) {
		return
	}
	snap, err := approvalSnapshot(a.dir)
	if err == nil {
		err = snap.combinedError()
	}
	switch {
	case err != nil:
		rec.Detail = err.Error()
	case pin == snap.legacy:
		rec.Pins = snap.digests
		rec.Detail = "legacy approval migrated"
	default:
		rec.Detail = "legacy approval differs from current hooks"
	}
}
