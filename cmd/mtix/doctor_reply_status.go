// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

// fixReplyPolicy is the exact fix for an unreadable reply limit.
const fixReplyPolicy = "mtix config set wake.remote_awake_daily_cap off; mtix config set wake.reply_extensions_per_hour off (each takes a positive integer, or off for unlimited)"

const fixSkippedReplyWindow = "re-send the comment with an author: set MTIX_AUTHOR_ID or pass author to mtix_annotate; the skipped record clears itself after a day"

// attachReplyStatus adds the open reply windows, active holds and the day's
// remote-held time to the sync.hub-contact row, in text and in --json
// (MTIX-112.20.3). An unreadable reply state is a warning with its fix; it
// never widens hub contact.
func attachReplyStatus(row *DoctorCheck, src *hubContactSource, now time.Time) {
	st, err := src.state.ReadReplyState(src.key())
	if err == nil {
		var pol service.ReplyPolicy
		if pol, err = service.ReadReplyPolicy(src.state); err == nil {
			status := service.ReplyStatusAt(st, pol, now)
			row.Reply = &status
			row.Detail += "; " + replyStatusText(status)
			if status.SkippedWindows > 0 {
				row.Warn = true
				row.Detail += fmt.Sprintf("; %d malformed reply window(s) skipped (no sender)", status.SkippedWindows)
				row.Fix = fixSkippedReplyWindow
			}
			return
		}
	}
	row.Warn = true
	row.Detail += "; reply windows unreadable (" + err.Error() + "): the cadence ignores them"
	row.Fix = fixReplyPolicy
	if dir, pathErr := src.state.Path(); pathErr == nil {
		row.Fix += "; if the limits are fine, move the damaged record " + filepath.Join(dir, "replywin-"+src.key()+".json") + " aside (it holds only open reply windows)"
	}
}

func replyStatusText(s service.ReplyStatus) string {
	capText := "uncapped"
	if s.DailyCapSecs > 0 {
		capText = fmt.Sprintf("cap %ds", s.DailyCapSecs)
		if s.CapSpent {
			capText += " spent"
		}
	}
	limit := "extensions unlimited"
	if s.ExtensionsPerHour > 0 {
		limit = fmt.Sprintf("extension limit %d/h per sender", s.ExtensionsPerHour)
	}
	return fmt.Sprintf("reply windows %d open, holds %d active, remote-held today %ds (%s; %s)",
		len(s.OpenWindows), len(s.ActiveHolds), s.RemoteHeldSecsToday, capText, limit)
}
