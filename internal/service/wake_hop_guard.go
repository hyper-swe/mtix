// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeSettingsReader supplies effective host policy without mutation (FR-20.3).
type WakeSettingsReader interface {
	WakeSettings(string) (map[string]model.WakeSetting, []oplocal.Report, error)
}

func (d *WakeDispatcher) guardWakeHop(ctx context.Context, key model.WakeKey) (int64, bool, error) {
	hop, err := d.store.WakeEventHop(ctx, key)
	if err != nil {
		return 0, true, fmt.Errorf("read candidate chain depth: %w", err)
	}
	if hop < 0 {
		return 0, true, fmt.Errorf("negative candidate hop: %w", model.ErrInvalidInput)
	}
	limit := int64(32)
	if d.settings != nil {
		values, reports, settingsErr := d.settings.WakeSettings(key.AgentID)
		if settingsErr != nil {
			return 0, true, fmt.Errorf("read wake hop policy: %w", settingsErr)
		}
		if len(reports) > 0 {
			return 0, true, fmt.Errorf("foreign host wake settings: %w", model.ErrOperatorStateUnreadable)
		}
		limit, err = strconv.ParseInt(values["wake.max_hop_depth"].Value, 10, 64)
		if err != nil || limit <= 0 {
			return 0, true, fmt.Errorf("hop guard must be positive: %w", model.ErrInvalidInput)
		}
	}
	if hop <= limit {
		return hop, false, nil
	}
	fix := "mtix config set wake.max_hop_depth " + strconv.FormatInt(hop, 10)
	if d.settings != nil {
		fix += " --agent " + key.AgentID
	}
	detail := fmt.Sprintf("held wake: hop depth %d exceeds %d; %s", hop, limit, fix)
	d.held(detail)
	_, err = d.alerts.Raise(ctx, AlertRequest{Agent: key.AgentID, Class: oplocal.AlertClassWakeHopDepth, Detail: detail, Fix: fix})
	if err != nil {
		return hop, true, fmt.Errorf("record hop guard trip: %w", err)
	}
	return hop, true, nil
}
