// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"log/slog"
	"sync"
)

// directoryWarner holds a logger and the set of directories already warned
// about, so the best-effort unsupported-directory-sync warning is emitted at
// most once per directory per service instance (MTIX-130.14). A nil warner,
// or one with a nil logger, is silent and records nothing.
type directoryWarner struct {
	logger *slog.Logger
	warned sync.Map
}

func newDirectoryWarner(logger *slog.Logger) *directoryWarner {
	return &directoryWarner{logger: logger}
}

// shouldWarn reports whether a warning for path must be logged now, recording
// the path only when a warning will actually be logged.
func (w *directoryWarner) shouldWarn(path string) bool {
	if w == nil || w.logger == nil {
		return false
	}
	_, already := w.warned.LoadOrStore(path, struct{}{})
	return !already
}
