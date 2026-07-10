// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppendFileAdapter appends one line per matched event to a project-local file
// (FR-19.3), giving an agent or human a plain-text audit trail of hook events.
// It runs async after the mutation commits and is non-fatal: any error is
// returned for the dispatcher to LOG, never propagated to the mutation.
//
// Security: the configured path is resolved RELATIVE to baseDir (the project
// root) and confined to it — an absolute path or a `..` sequence that would
// escape baseDir is rejected, so an adapter can never write outside the project
// (FR-19.3).
type AppendFileAdapter struct {
	baseDir string
}

// NewAppendFileAdapter returns an adapter that resolves configured paths under
// baseDir (the project root).
func NewAppendFileAdapter(baseDir string) *AppendFileAdapter {
	return &AppendFileAdapter{baseDir: baseDir}
}

// Name implements Adapter.
func (a *AppendFileAdapter) Name() string { return AdapterAppendFile }

// Deliver appends a single "<RFC3339>\t<event.Name>\t<event.NodeID>\t<EventJSON>\n"
// line to the resolved path, opening O_APPEND|O_CREATE and closing per call so
// concurrent deliveries interleave cleanly rather than truncate.
func (a *AppendFileAdapter) Deliver(_ context.Context, d Delivery) error {
	if d.Hook.AppendFile == nil || d.Hook.AppendFile.Path == "" {
		return fmt.Errorf("append-file: hook %q has no append-file.path", d.Hook.Name)
	}
	path, err := a.resolve(d.Hook.AppendFile.Path)
	if err != nil {
		return err
	}

	line := fmt.Sprintf("%s\t%s\t%s\t%s\n",
		time.Now().UTC().Format(time.RFC3339), d.Event.Name, d.Event.NodeID, d.EventJSON)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append-file: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("append-file: write %s: %w", path, err)
	}
	return nil
}

// resolve confines rel to a.baseDir, rejecting absolute paths and any `..`
// traversal that would escape the project root.
func (a *AppendFileAdapter) resolve(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("append-file: path %q must be relative to the project", rel)
	}
	base := filepath.Clean(a.baseDir)
	joined := filepath.Join(base, rel) // Join cleans, collapsing any interior `..`

	within, err := filepath.Rel(base, joined)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("append-file: path %q escapes the project directory", rel)
	}
	return joined, nil
}
