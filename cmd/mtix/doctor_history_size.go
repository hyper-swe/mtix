// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// historySizeCheckID is the id of the history size doctor row.
const historySizeCheckID = "history.size"

// historyLimitKey is the config key of the per-task history limit.
const historyLimitKey = "history.max_entries_per_task"

// historySizeTop is how many of the largest tasks the check lists.
const historySizeTop = 5

// registerHistorySizeCheck adds the history size check (MTIX-95.31.19.4) when
// the board has history or a limit: rows, tasks, the largest tasks and, when
// history.max_entries_per_task is set, how many tasks exceed it. The size is
// computed once, here, and the row reports that one report. The row is info,
// or warn when a task is over the limit (lowering the limit trims only at the
// next change to a task). The numbers are in the row's data object for --json.
func registerHistorySizeCheck(svc *service.DoctorService) error {
	if app.store == nil || app.configSvc == nil {
		return nil
	}
	limit := app.configSvc.HistoryMaxEntries()
	rep, err := app.store.HistorySize(context.Background(), historySizeTop, limit)
	raw, _ := app.configSvc.Get(historyLimitKey)
	unreadable := limit == 0 && strings.TrimSpace(raw) != ""
	if err == nil && rep.Rows == 0 && limit == 0 && !unreadable {
		return nil // nothing to report; the row would only change an unconfigured checkout's report
	}
	row := historySizeRow(rep, limit, err)
	if unreadable {
		// A hand-edited config.yaml bypasses the check mtix config set makes, and
		// the value is read as unset: nothing is trimmed. Say so, with the fix.
		row.Status = service.DoctorWarn
		row.Detail += fmt.Sprintf("; %s is set to %q, which is not a positive whole number, so no limit applies", historyLimitKey, raw)
		row.Fix = "mtix config set " + historyLimitKey + " <N> with a positive whole number, or mtix config set " + historyLimitKey + " \"\" to unset it"
	}
	return svc.Register(service.DoctorCheck{ID: historySizeCheckID, Run: func(context.Context) service.DoctorResult { return row }})
}

func historySizeRow(rep sqlite.HistorySizeReport, limit int, err error) service.DoctorResult {
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: fmt.Sprintf("could not read the task history size: %v", err)}
	}
	var configured any
	if limit > 0 {
		configured = limit
	}
	row := service.DoctorResult{Status: service.DoctorInfo,
		Detail: fmt.Sprintf("%d history rows across %d tasks", rep.Rows, rep.Tasks),
		Data: map[string]any{"rows": rep.Rows, "tasks": rep.Tasks, "largest_tasks": rep.Largest,
			"max_entries_per_task": configured, "oversize_tasks": rep.Oversize, "ahead_of_clock": rep.Ahead}}
	if rep.Ahead > 0 {
		row.Detail += fmt.Sprintf("; %d change entries are dated after this machine's clock (not counted toward the limit or trimmed until the clock passes them)", rep.Ahead)
	}
	if rep.Oversize > 0 {
		row.Status = service.DoctorWarn
		row.Detail += fmt.Sprintf("; %d tasks hold more than %d change entries (each is trimmed at its next change)", rep.Oversize, limit)
		row.Fix = "no action is needed: the next change to each task trims it; to keep more entries, raise the limit with mtix config set history.max_entries_per_task <N>"
	}
	return row
}
