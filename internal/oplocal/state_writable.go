// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/model"
)

// ProbeWritable checks, by writing, that this process can create files in the operator
// directory that holds the mirror and the alert log (ADR-007 section 2.7 step 0, A4). It
// does not trust a sandbox marker: it creates an exclusive owner-only file, closes and
// removes it. A sandboxed trigger therefore learns it cannot record a wake before it
// claims one. The directory is created first when absent, like every operator write.
func (s *State) ProbeWritable() (err error) {
	if err = s.Ensure(); err != nil {
		return fmt.Errorf("probe operator state: %w", err)
	}
	dir, err := s.resolve()
	if err != nil {
		return fmt.Errorf("probe operator state: %w", err)
	}
	// os.CreateTemp opens with O_EXCL and mode 0600, so it never reuses another file.
	create := s.probeCreate
	if create == nil {
		create = os.CreateTemp
	}
	f, err := create(dir, ".write-probe-")
	if err != nil {
		return fmt.Errorf("probe operator state: %w: %w", err, model.ErrOperatorStateUnreadable)
	}
	name := f.Name()
	err = errors.Join(f.Close(), os.Remove(name))
	if err != nil {
		return fmt.Errorf("probe operator state cleanup: %w: %w", err, model.ErrOperatorStateUnreadable)
	}
	return nil
}
