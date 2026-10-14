// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync"
)

// FirstHistoryVersion is the first mtix release that keeps task history. A
// board last written by an earlier release carries none (MTIX-95.31.19.9).
// Raise it in the same change whenever the history format changes in a way
// that older writers would lose. Never compare against a bare version literal.
const FirstHistoryVersion = "0.6.0"

// OlderWriterWarning returns the warning an import prints when history is in
// play (historyOn: history.export is on for this board) and the board file
// was last written before FirstHistoryVersion, or by an mtix that recorded no
// version, and whether it applies (MTIX-95.31.19.9, ADR-006 A3). Such a
// writer exports no task history, so the board may lack entries. It is
// silent with the switch off, for a writer at or above FirstHistoryVersion
// (a pre-release of it included, so a patch gap never warns), for a file
// version that cannot be read (a dev build), and when this binary's own
// version cannot be read. The message names a version, never board content.
func OlderWriterWarning(fileVersion, binaryVersion string, historyOn bool) (string, bool) {
	if !historyOn {
		return "", false
	}
	if _, err := sync.ParseVersion(binaryVersion); err != nil {
		return "", false
	}
	if fileVersion == "" {
		return "history may be incomplete: board last written by mtix of an unrecorded version; " +
			"upgrade mtix on that machine", true
	}
	fv, err := sync.ParseVersion(fileVersion)
	if err != nil {
		return "", false
	}
	first, _ := sync.ParseVersion(FirstHistoryVersion)
	fv.PreRelease = "" // 0.6.0-beta already keeps history
	if fv.Compare(first) >= 0 {
		return "", false
	}
	return fmt.Sprintf("history may be incomplete: board last written by mtix %s; upgrade mtix on that machine",
		fileVersion), true
}

// SetBinaryVersion sets the version of this mtix build, which the older-writer
// warning compares an imported file's last writer with (MTIX-95.31.19.9).
func (s *SyncService) SetBinaryVersion(v string) { s.binaryVersion = v }

// SetHistoryExportSource installs the reader of the history.export switch, which
// the older-writer warning depends on; nil, the default, means off.
func (s *SyncService) SetHistoryExportSource(on func() bool) { s.historyExport = on }

// SetJSONNotices makes the older-writer warning one JSON line on the notice
// writer instead of a text line, for --json (MTIX-95.31.19.9).
func (s *SyncService) SetJSONNotices(on bool) { s.jsonNotices = on }

// noticeOlderWriter writes the older-writer warning for one imported file:
// the text line, or with SetJSONNotices one line
// {"warning":"history_incomplete","writer_version":...,"message":...}.
func (s *SyncService) noticeOlderWriter(writerVersion string) {
	msg, ok := OlderWriterWarning(writerVersion, s.binaryVersion, s.historyExport != nil && s.historyExport())
	if !ok {
		return
	}
	if !s.jsonNotices {
		fmt.Fprintf(s.notices, "mtix: warning: %s\n", msg)
		return
	}
	line, err := json.Marshal(struct {
		Warning       string `json:"warning"`
		WriterVersion string `json:"writer_version"`
		Message       string `json:"message"`
	}{"history_incomplete", writerVersion, msg})
	if err != nil {
		s.logger.Warn("could not encode the older-writer warning", "error", err)
		return
	}
	fmt.Fprintln(s.notices, string(line))
}

// noticeHistoryImport reports what an automatic import did with the file's
// task history beyond storing it: entries that conflicted or were skipped.
// One text line per warning, or with SetJSONNotices one JSON line with the
// counts. Counts and entry ids only, never content (MTIX-95.31.19.9).
func (s *SyncService) noticeHistoryImport(res *sqlite.ImportResult) {
	if res == nil || len(res.Warnings) == 0 {
		return
	}
	if !s.jsonNotices {
		for _, w := range res.Warnings {
			fmt.Fprintf(s.notices, "mtix: warning: %s\n", w)
		}
		return
	}
	line, err := json.Marshal(struct {
		Warning       string `json:"warning"`
		Conflicts     int    `json:"history_conflicts"`
		SkippedLegacy int    `json:"history_skipped_legacy_key"`
		Message       string `json:"message"`
	}{"history_import", res.HistoryConflicts, res.HistorySkippedLegacyKey, strings.Join(res.Warnings, "; ")})
	if err != nil {
		s.logger.Warn("could not encode the history import warning", "error", err)
		return
	}
	fmt.Fprintln(s.notices, string(line))
}
