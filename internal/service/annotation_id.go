// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// annotationIDs mints annotation ids that ascend with creation order, even
// for annotations created in the same millisecond (MTIX-100.2). Annotations
// are listed by (created_at, id), so two annotations that share a time must
// keep their creation order in their ids; random ULID entropy alone would
// order them by chance. The monotonic entropy is not safe for concurrent use,
// so a mutex guards it. It is held per service, not package-wide.
type annotationIDs struct {
	mu      sync.Mutex
	entropy io.Reader
}

// newAnnotationIDs returns a generator drawing its entropy from source.
func newAnnotationIDs(source io.Reader) *annotationIDs {
	return &annotationIDs{entropy: ulid.Monotonic(source, 0)}
}

// next returns a ULID for an annotation created at t. Two ids minted in the
// same millisecond ascend in the order they were minted.
func (g *annotationIDs) next(t time.Time) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, err := ulid.New(ulid.Timestamp(t), g.entropy)
	if err != nil {
		return "", fmt.Errorf("generate annotation ULID: %w", err)
	}
	return id.String(), nil
}
