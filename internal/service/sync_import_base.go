// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Base sources, as the refusal and the notice name them.
const (
	baseSourceRecord = "the last-exchanged record"
	baseSourceBoard  = "the git merge-base board"
)

// recordBase returns the base the last-exchanged record makes (FIRST source,
// MTIX-95.31.18, read as LastExchanged reads it). found is true when a valid record exists, whatever its form
// (a record in the current form is the base and the older-form rule does not
// apply). Otherwise why says why it is missing.
func (s *SyncService) recordBase(ctx context.Context, mtixDir string) (base *sqlite.ImportBase, found bool, why string) {
	rec, err := s.lastExchangedHeld(ctx, mtixDir) // the import holds the exclusive sync lock
	if err != nil {
		category := "unavailable"
		var re *exchangeRecordError
		if errors.As(err, &re) {
			category = re.category
		}
		return nil, false, "no usable exchange record (" + category + ")"
	}
	tasks := make([]sqlite.BaseTask, 0, len(rec.Tasks))
	for _, t := range rec.Tasks {
		tasks = append(tasks, sqlite.BaseTask{Kind: t.Kind, Identity: t.Identity, ID: t.ID, Fields: t.Fields})
	}
	return sqlite.NewImportBase(baseSourceRecord, rec.BoardForm == "schema-1.0.0", tasks), true, ""
}

// boardBase returns the base the git merge-base board makes (SECOND source,
// MTIX-95.31.20), or why none could be obtained, after the record's reason.
func boardBase(board *sqlite.ExportData, err error, recordWhy string) (*sqlite.ImportBase, string) {
	if err != nil {
		return nil, recordWhy + "; no git merge-base board (" + err.Error() + ")"
	}
	base, baseErr := sqlite.ImportBaseFromBoard(board, baseSourceBoard)
	if baseErr != nil {
		return nil, recordWhy + "; the git merge-base board could not be used"
	}
	return base, ""
}

// resolveOnBase settles the check against base when base is in an older
// form, else nil. A file that fails validation, or a resolution that errors,
// is not resolved: the caller refuses, the safe side.
func (s *SyncService) resolveOnBase(base *sqlite.ImportBase, c *importCheck) *sqlite.BaseResolution {
	if base == nil || !base.Older {
		return nil
	}
	if err := sqlite.ValidateExport(c.incoming); err != nil {
		return nil
	}
	res, err := sqlite.ResolveWithBase(c.local, c.incoming, base)
	if err != nil {
		s.logger.Warn("could not resolve the pulled board against the base", "source", base.Source, "error", err)
		return nil
	}
	return res
}

// baseNote explains a refusal that the base order could not settle: which
// sources were missing and why, or which base was used and what is left.
func baseNote(base *sqlite.ImportBase, res *sqlite.BaseResolution, why string) string {
	switch {
	case base == nil:
		return "; mtix could not tell your changes from your teammate's, because " + why +
			". Upgrading from mtix 0.5.3 or older needs one of them as the starting point (a pull from git sets it up)"
	case res != nil:
		return "; compared with " + base.Source + " task by task: the tasks listed changed on both sides or hold other local data"
	}
	return ""
}

// noteLine renders note as its own line, or nothing.
func noteLine(note string) string {
	if note == "" {
		return ""
	}
	return "mtix: " + strings.TrimPrefix(note, "; ") + "\n"
}

// noticeResolved says that the import compared the pulled board with a base
// and what it kept from the local store (FR-15.2k: never silent).
func (s *SyncService) noticeResolved(res *sqlite.BaseResolution) {
	if res == nil {
		return
	}
	k := res.Kept
	fmt.Fprintf(s.notices, "mtix: told your changes from your teammate's with %s; kept here: %s, %s, %s, "+
		"%s changed since the last exchange and %s made since\n",
		res.Source, plural(k.Annotations, "annotation", "annotations"),
		plural(k.Activity, "activity entry", "activity entries"), plural(k.Fields, "field value", "field values"),
		plural(k.LocalChanges, "task field", "task fields"), plural(k.LocalTasks, "task", "tasks"))
}
