// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ErrWakeProtocolUnsupported refuses a wake pass that requires a newer binary.
var ErrWakeProtocolUnsupported = errors.New("wake dispatch protocol is newer than this binary")

func (d *WakeDispatcher) checkWakeProtocol(ctx context.Context) error {
	floor, err := d.store.MinDispatchProtocol(ctx)
	if err != nil {
		return d.raiseProtocolStop(ctx, "Cannot read the board dispatch protocol; no wakes were sent.", fmt.Errorf("read wake dispatch protocol: %w", err))
	}
	if floor < 0 {
		return d.raiseProtocolStop(ctx, "The board dispatch protocol is invalid; no wakes were sent.", model.ErrInvalidInput)
	}
	if floor > model.AttachProtocol {
		detail := fmt.Sprintf("The board requires attach protocol %d; this binary supports protocol %d. No wakes were sent.", floor, model.AttachProtocol)
		return d.raiseProtocolStop(ctx, detail, ErrWakeProtocolUnsupported)
	}
	return nil
}

func (d *WakeDispatcher) raiseProtocolStop(ctx context.Context, detail string, cause error) error {
	_, err := d.alerts.Raise(ctx, AlertRequest{Agent: d.alertAgent, Class: "wake.version-skew", Detail: detail,
		Fix: "Install a binary supporting the board dispatch protocol, restart the dispatcher, then run mtix doctor."})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("record wake protocol alert: %w", err))
	}
	return cause
}
