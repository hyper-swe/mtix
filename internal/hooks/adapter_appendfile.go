// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
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

// AppendFileIssue is one finding about a configured append-file path: why
// delivery would refuse it (Refused) or what to tighten (a warning), and the
// exact remedy. It is the one shape shared by delivery and `mtix hooks status`
// (TM I34), so the two cannot drift.
type AppendFileIssue struct {
	Hook    string `json:"hook"`
	Path    string `json:"path"`
	Reason  string `json:"reason"`
	Fix     string `json:"fix"`
	Refused bool   `json:"refused"`
}

// Deliver appends a single "<RFC3339>\t<event.Name>\t<event.NodeID>\t<EventJSON>\n"
// line to the resolved path, opening O_APPEND|O_CREATE through a root handle on
// the project so containment holds at open time, and closing per call so
// concurrent deliveries interleave cleanly rather than truncate.
func (a *AppendFileAdapter) Deliver(_ context.Context, d Delivery) error {
	if d.Hook.AppendFile == nil || d.Hook.AppendFile.Path == "" {
		return fmt.Errorf("append-file: hook %q has no append-file.path", d.Hook.Name)
	}
	if err := lineFieldProblem(d); err != nil {
		return err
	}
	root, err := os.OpenRoot(a.baseDir)
	if err != nil {
		return fmt.Errorf("append-file: open project directory: %w", err)
	}
	defer root.Close()
	name, pre, issue := inspectAppend(root, a.baseDir, d.Hook.AppendFile.Path)
	if issue != nil {
		return fmt.Errorf("append-file: %s", issue.Reason)
	}
	f, err := openAppendFile(root, name, pre)
	if err != nil {
		return err
	}
	defer f.Close()
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n",
		time.Now().UTC().Format(time.RFC3339), d.Event.Name, d.Event.NodeID, d.EventJSON)
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("append-file: write %s: %w", name, err)
	}
	return nil
}

// openAppendFile opens name inside root for appending. The root handle keeps
// every component inside the project. An existing target must still be the
// file that the earlier Lstat saw (pre), which a link swapped in afterwards is
// not; a target that did not exist is created exclusively, so a link or file
// that appears first makes the open fail. Opening never blocks on a pipe, and
// new files are 0600. The opened file must be regular with a single link.
func openAppendFile(root *os.Root, name string, pre os.FileInfo) (*os.File, error) {
	flags := os.O_APPEND | os.O_WRONLY | nonBlockFlag
	if pre == nil {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := root.OpenFile(name, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("append-file: open %s inside the project: %w", name, err)
	}
	if err := verifyOpened(f, pre); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("append-file: %s: %w", name, err)
	}
	if err := clearNonBlock(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("append-file: %s: %w", name, err)
	}
	return f, nil
}

// verifyOpened refuses an opened file that is not a regular file, has more
// than one link, or is not the file that was inspected before the open.
func verifyOpened(f *os.File, pre os.FileInfo) error {
	info, err := f.Stat()
	switch {
	case err != nil:
		return fmt.Errorf("inspect opened file: %w", err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("not a regular file; refused")
	case linkCount(info) > 1:
		return fmt.Errorf("has more than one hard link; refused")
	case pre != nil && !os.SameFile(pre, info):
		return fmt.Errorf("changed while it was being opened; refused")
	}
	return nil
}

// badLineRune reports a rune that could end or reshape a log line.
func badLineRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Cf, r)
}

// lineFieldProblem refuses an event whose name or node id holds a control or
// line-separator character, or whose payload holds a line break, so one event
// is one line.
func lineFieldProblem(d Delivery) error {
	for _, field := range []string{d.Event.Name, d.Event.NodeID} {
		if strings.ContainsFunc(field, badLineRune) {
			return fmt.Errorf("append-file: event field holds a control or line-separator character; refused")
		}
	}
	if bytes.ContainsAny(d.EventJSON, "\r\n") || bytes.Contains(d.EventJSON, []byte("\u2028")) || bytes.Contains(d.EventJSON, []byte("\u2029")) {
		return fmt.Errorf("append-file: event payload holds a line break; refused")
	}
	return nil
}

// inspectAppend checks rel against the project root handle and returns the
// root-relative name to open, the Lstat of an existing target (nil when it does
// not exist) and the refusal, if any. Containment of every parent component is
// enforced by the root handle itself.
func inspectAppend(root *os.Root, baseDir, rel string) (string, os.FileInfo, *AppendFileIssue) {
	refuse := func(reason, fix string) (string, os.FileInfo, *AppendFileIssue) {
		return "", nil, &AppendFileIssue{Path: rel, Reason: reason, Fix: fix, Refused: true}
	}
	joined, err := (&AppendFileAdapter{baseDir: baseDir}).resolve(rel)
	if err != nil {
		return refuse(strings.TrimPrefix(err.Error(), "append-file: "), "set append-file.path to a relative path to a regular file inside the project")
	}
	name, err := filepath.Rel(filepath.Clean(baseDir), joined)
	if err != nil {
		return refuse(fmt.Sprintf("path %q cannot be made relative to the project", rel), "set append-file.path to a relative path inside the project")
	}
	info, err := root.Lstat(name)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return refuse(fmt.Sprintf("%q is a link; refused", rel), fmt.Sprintf("remove the link %s, or point append-file.path at a regular file inside the project", rel))
	case err == nil && !info.Mode().IsRegular():
		return refuse(fmt.Sprintf("%q is not a regular file; refused", rel), fmt.Sprintf("remove %s, or point append-file.path at a regular file inside the project", rel))
	case err == nil && linkCount(info) > 1:
		return refuse(fmt.Sprintf("%q has more than one hard link; refused", rel), fmt.Sprintf("replace %s with a regular file that has a single link, or choose another path", rel))
	case err != nil && !os.IsNotExist(err):
		return refuse(fmt.Sprintf("path %q leads outside the project through a link, or cannot be inspected: %v", rel, err), "remove the link that leaves the project, or point append-file.path at a regular file inside it")
	}
	return name, info, nil
}

// AppendFileIssues lists what is wrong with the append-file path of h under
// baseDir (TM I34): a refusal delivery would apply, or an existing file whose
// permissions are wider than owner-only (delivery still writes to it).
func AppendFileIssues(baseDir string, h Hook) []AppendFileIssue {
	if h.AppendFile == nil || h.AppendFile.Path == "" {
		return nil
	}
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return []AppendFileIssue{{Hook: h.Name, Path: h.AppendFile.Path, Reason: fmt.Sprintf("project directory cannot be opened: %v", err), Fix: "check the project directory", Refused: true}}
	}
	defer root.Close()
	_, info, issue := inspectAppend(root, baseDir, h.AppendFile.Path)
	if issue != nil {
		issue.Hook = h.Name
		return []AppendFileIssue{*issue}
	}
	if info != nil && wideMode(info) {
		return []AppendFileIssue{{Hook: h.Name, Path: h.AppendFile.Path, Reason: fmt.Sprintf("%q is readable or writable by group or others; delivery still writes to it", h.AppendFile.Path), Fix: "chmod 600 " + h.AppendFile.Path}}
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
