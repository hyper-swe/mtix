// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// ArtifactState is the condition of one release-defined attach artifact.
type ArtifactState int

// Artifact states reported to doctor (ADR-007 §7 setup.shim, glue.<h>).
const (
	// ArtifactAbsent means nothing is installed on this host.
	ArtifactAbsent ArtifactState = iota + 1
	// ArtifactCurrent means the installed bytes match this release.
	ArtifactCurrent
	// ArtifactStale means a rewrite to this release's bytes repairs it.
	ArtifactStale
	// ArtifactRefused means a rewrite cannot repair it; Detail says why.
	ArtifactRefused
)

// ArtifactStatus describes one artifact for doctor and the refresh pass.
type ArtifactStatus struct {
	State  ArtifactState
	Path   string
	Detail string
	// Fix is the exact remedy when State is ArtifactRefused.
	Fix string
}

// Writer rewrites one release-defined artifact. Its bytes carry no authority,
// so any non-sandboxed command and doctor --fix may call Write without a
// terminal (ADR-007 §6.5). A Writer never writes a grant.
type Writer interface {
	Name() string
	Check(ctx context.Context) (ArtifactStatus, error)
	Write(ctx context.Context) error
}

// Registry is the set of artifact writers this build ships.
type Registry struct {
	current int
	writers []Writer
}

// NewRegistry validates and orders the writers for a binary at protocol current.
func NewRegistry(current int, writers ...Writer) (*Registry, error) {
	if current < 1 {
		return nil, fmt.Errorf("attach registry protocol %d: %w", current, model.ErrInvalidInput)
	}
	seen := map[string]bool{}
	for _, w := range writers {
		if w == nil || w.Name() == "" || seen[w.Name()] {
			return nil, fmt.Errorf("attach registry writer: %w", model.ErrInvalidInput)
		}
		seen[w.Name()] = true
	}
	return &Registry{current: current, writers: writers}, nil
}

// Writer returns the registered writer with name, or nil.
func (r *Registry) Writer(name string) Writer {
	for _, w := range r.writers {
		if w.Name() == name {
			return w
		}
	}
	return nil
}

// RefreshReport lists what a refresh pass rewrote and what it had to leave.
type RefreshReport struct {
	Rewritten []string
	Pending   []string
}

// Refresh rewrites glue marked for refresh and every artifact whose check
// reports it stale. Glue newer than this binary, or glue with no writer in
// this build, stays marked. Errors are joined; a failed rewrite keeps its mark.
func (r *Registry) Refresh(ctx context.Context, st RecordStore) (RefreshReport, error) {
	var report RefreshReport
	pending, err := PendingGlueRefresh(st)
	if err != nil {
		return report, err
	}
	harnesses := make([]string, 0, len(pending))
	for h := range pending {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)
	var errs []error
	for _, h := range harnesses {
		w := r.Writer("glue." + h)
		if w == nil || pending[h] > r.current {
			report.Pending = append(report.Pending, "glue."+h)
			continue
		}
		if err := w.Write(ctx); err != nil {
			errs = append(errs, fmt.Errorf("refresh glue.%s: %w", h, err))
			continue
		}
		report.Rewritten = append(report.Rewritten, w.Name())
		errs = append(errs, ClearGlueRefresh(st, h))
	}
	errs = append(errs, r.rewriteStale(ctx, &report))
	return report, errors.Join(errs...)
}

func (r *Registry) rewriteStale(ctx context.Context, report *RefreshReport) error {
	var errs []error
	for _, w := range r.writers {
		status, err := w.Check(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("check %s: %w", w.Name(), err))
			continue
		}
		if status.State != ArtifactStale {
			continue
		}
		if err := w.Write(ctx); err != nil {
			errs = append(errs, fmt.Errorf("refresh %s: %w", w.Name(), err))
			continue
		}
		report.Rewritten = append(report.Rewritten, w.Name())
	}
	return errors.Join(errs...)
}
