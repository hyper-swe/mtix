// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// serveRecordFile is the serve-address record inside .mtix/ (MTIX-232). It is
// read only by mtix url. It is deliberately NOT the PID lock (mtix.pid): that
// file switches on REST routing of CLI commands (FR-14.1b), which serve must
// not do as a side effect of being discoverable.
const serveRecordFile = "serve.json"

// serveHealthTimeout bounds the liveness probe so mtix url never hangs.
const serveHealthTimeout = 750 * time.Millisecond

// serveRecord is the content of the serve-address record.
type serveRecord struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Host      string `json:"host"`
	StartedAt string `json:"started_at,omitempty"`
}

// publishServeRecord writes the record atomically (temp file, fsync, rename)
// with owner-only permissions. startedAt comes from the caller's injected
// clock; this function never reads the wall clock.
func publishServeRecord(mtixDir string, pid int, host string, port int, startedAt time.Time) (err error) {
	data, err := json.Marshal(serveRecord{
		PID: pid, Port: port, Host: host, StartedAt: startedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("encode serve record: %w", err)
	}
	tmp, err := os.CreateTemp(mtixDir, ".serve-*.tmp")
	if err != nil {
		return fmt.Errorf("create serve record temp file: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("restrict serve record: %w", err), tmp.Close())
	}
	if _, err = tmp.Write(append(data, '\n')); err != nil {
		return errors.Join(fmt.Errorf("write serve record: %w", err), tmp.Close())
	}
	if err = tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync serve record: %w", err), tmp.Close())
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close serve record: %w", err)
	}
	if err = os.Rename(tmp.Name(), filepath.Join(mtixDir, serveRecordFile)); err != nil {
		return fmt.Errorf("publish serve record: %w", err)
	}
	return nil
}

// loadServeRecord parses and validates the record file without any liveness
// check. ok is false when the file is missing, unreadable, corrupt or invalid.
func loadServeRecord(mtixDir string) (serveRecord, bool) {
	var rec serveRecord
	data, err := os.ReadFile(filepath.Join(mtixDir, serveRecordFile))
	if err != nil || json.Unmarshal(data, &rec) != nil {
		return serveRecord{}, false
	}
	if rec.PID <= 0 || rec.Port < 1 || rec.Port > 65535 {
		return serveRecord{}, false
	}
	return rec, true
}

// removeServeRecord removes the record only when it still belongs to pid, so
// a stopping serve never deletes the record of another live serve.
func removeServeRecord(mtixDir string, pid int) error {
	if rec, ok := loadServeRecord(mtixDir); ok && rec.PID == pid {
		return removeRecordFile(mtixDir)
	}
	return nil
}

// removeRecordFile deletes the record file. A file that is already gone is not
// an error; any other failure is returned with context.
func removeRecordFile(mtixDir string) error {
	if err := os.Remove(filepath.Join(mtixDir, serveRecordFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove serve record: %w", err)
	}
	return nil
}

// removeServeRecordIfUnchanged removes the record only if it still holds the
// pid and start time that were read, so a stale-record cleanup never deletes
// the record a new serve published in the meantime. The re-read narrows the
// window to one stat-and-unlink; it cannot close it entirely.
func removeServeRecordIfUnchanged(mtixDir string, seen serveRecord) error {
	now, ok := loadServeRecord(mtixDir)
	if !ok || now.PID != seen.PID || now.StartedAt != seen.StartedAt {
		return nil
	}
	return removeRecordFile(mtixDir)
}

// readServeRecord returns the record of a running serve. A record whose
// process is dead is stale: it is removed and reported as not running. A
// corrupt record, or a live PID whose port does not answer like an mtix serve
// (PID reuse, failed bind), is reported as not running and left in place.
func readServeRecord(mtixDir string) (serveRecord, bool) {
	rec, ok := loadServeRecord(mtixDir)
	if !ok {
		return serveRecord{}, false
	}
	if !isProcessAlive(rec.PID) {
		// Best-effort cleanup of a stale file: the answer (not running) holds
		// either way, so a failure is logged, not returned.
		if err := removeServeRecordIfUnchanged(mtixDir, rec); err != nil {
			slog.Default().Debug("stale serve record not removed", "error", err)
		}
		return serveRecord{}, false
	}
	if !probeServeHealth(rec) {
		return serveRecord{}, false
	}
	return rec, true
}

// isWildcardHost reports whether a bind host is empty or a wildcard address.
func isWildcardHost(host string) bool {
	switch strings.Trim(host, "[]") {
	case "", "0.0.0.0", "::":
		return true
	}
	return false
}

// probeHost is the address to dial for a recorded bind host (unbracketed).
func probeHost(host string) string {
	if isWildcardHost(host) {
		return "127.0.0.1"
	}
	return strings.Trim(host, "[]")
}

// serveLinkHost is the host url prints: the bound host unless it is a
// wildcard, then 127.0.0.1; IPv6 literals are bracketed for use in a URL.
func serveLinkHost(host string) string {
	h := probeHost(host)
	if strings.Contains(h, ":") {
		return "[" + h + "]"
	}
	return h
}

// authRequiredCode is the error code mtix serve puts in its 401 body when a
// bearer token is required (AuthMiddleware, MTIX-220).
const authRequiredCode = "AUTH_REQUIRED"

// probeServeHealth asks the recorded address for /health. An mtix serve
// answers 200 with status ok, or, when a bearer token guards it, 401 with the
// mtix AUTH_REQUIRED error code. A foreign service that merely answers 401 or
// 403, a closed port or any other status is not our serve.
func probeServeHealth(rec serveRecord) bool {
	target := "http://" + net.JoinHostPort(probeHost(rec.Host), strconv.Itoa(rec.Port)) + "/health"
	client := &http.Client{Timeout: serveHealthTimeout}
	resp, err := client.Get(target) //nolint:noctx // bounded by Client.Timeout; loopback or operator-bound address from our own record
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Status string `json:"status"`
		Error  struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body.Status == "ok"
	case http.StatusUnauthorized:
		return body.Error.Code == authRequiredCode
	}
	return false
}

// announceServe publishes the serve-address record for this process and
// returns the function that removes it. A failure never stops serve: it is
// logged and serve runs undiscoverable. Another live serve's record is never
// overwritten (MTIX-232).
func announceServe(cfg serveAnnounceConfig) func() {
	noop := func() {}
	mtixDir, err := findMtixDir()
	if err != nil {
		return noop
	}
	port, err := strconv.Atoi(cfg.port)
	if err != nil {
		cfg.logger.Warn("serve record not published: bad port", "port", cfg.port)
		return noop
	}
	pid := os.Getpid()
	if existing, ok := readServeRecord(mtixDir); ok && existing.PID != pid {
		cfg.logger.Warn("serve record not published: another serve is already recorded", "pid", existing.PID)
		return noop
	}
	if err := publishServeRecord(mtixDir, pid, cfg.host, port, cfg.now()); err != nil {
		cfg.logger.Warn("serve record not published", "error", err)
		return noop
	}
	return func() {
		if err := removeServeRecord(mtixDir, pid); err != nil {
			cfg.logger.Warn("serve record not removed", "error", err)
		}
	}
}

// serveAnnounceConfig carries what announceServe needs from the serve setup.
type serveAnnounceConfig struct {
	host, port string
	now        func() time.Time
	logger     *slog.Logger
}
