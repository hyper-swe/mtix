// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/hyper-swe/mtix/internal/attach"
)

// hostServiceManager is used only after the existing harness and writable-state restart gates.
type hostServiceManager struct{}

func (hostServiceManager) Restart(ctx context.Context) error {
	state, err := operatorState()
	if err != nil {
		return err
	}
	if !hostDispatcherInstalled(state) {
		return attach.ErrNoDispatcherService
	}
	spec, err := currentHostServiceSpec(5)
	if err != nil {
		return err
	}
	return restartHostService(ctx, runtime.GOOS, spec, func(ctx context.Context, argv [][]string) error { return runServiceCmds(ctx, io.Discard, argv, true) })
}

func restartHostService(ctx context.Context, goos string, spec *serviceSpec, run func(context.Context, [][]string) error) error {
	argv := spec.Start
	switch goos {
	case "linux":
		argv = [][]string{{"systemctl", "--user", "restart", spec.Label}}
	case "darwin":
	case "windows":
		argv = append(append([][]string{}, spec.Stop...), spec.Start...)
	default:
		return fmt.Errorf("unsupported dispatcher service manager %q", goos)
	}
	if err := run(ctx, argv); err != nil {
		return fmt.Errorf("restart host dispatcher service: %w", err)
	}
	return nil
}
