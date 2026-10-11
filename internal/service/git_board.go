// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Optional committed-board reads belong to the import service, not repository glue.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ErrNoMergeBase means an optional ancestor source is unavailable; no board was read safely.
var ErrNoMergeBase = errors.New("ancestor board unavailable")

const gitBoardTimeout = 5 * time.Second

// GitBoardProvenance describes the selected graph source, not incoming-file lineage.
type GitBoardProvenance struct {
	Source        string
	Candidates    [2]string
	BaseOID       string
	Form          string
	BlobSHA256    string
	ContentSHA256 string
}

// MergeBaseBoard reads a validated committed board without touching the local store.
// A future import loss check must establish incoming lineage before using this source.
func (s *SyncService) MergeBaseBoard(ctx context.Context, root string) (*sqlite.ExportData, GitBoardProvenance, error) {
	if s.MaxImportSize <= 0 {
		return nil, GitBoardProvenance{}, gitBoardError("invalid size cap", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, gitBoardTimeout)
	defer cancel()
	path, err := selectBoardGit()
	if err != nil {
		return nil, GitBoardProvenance{}, err
	}
	return readMergeBaseBoard(ctx, root, path, s.MaxImportSize)
}

func gitBoardError(reason string, cause error) error {
	// Only fixed categories and safe context sentinels can cross this boundary.
	if errors.Is(cause, context.Canceled) {
		return fmt.Errorf("%w: %s: %w", ErrNoMergeBase, reason, context.Canceled)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: %w", ErrNoMergeBase, reason, context.DeadlineExceeded)
	}
	return fmt.Errorf("%w: %s", ErrNoMergeBase, reason)
}

func readMergeBaseBoard(ctx context.Context, root, path string, capBytes int64) (*sqlite.ExportData, GitBoardProvenance, error) {
	dir, err := boardRepository(root)
	if err != nil {
		return nil, GitBoardProvenance{}, err
	}
	oidBytes, err := readGit(ctx, path, dir, 4096, "merge-base", "--all", "ORIG_HEAD", "@{upstream}")
	if err != nil {
		return nil, GitBoardProvenance{}, err
	}
	oid, err := boardOID(oidBytes)
	if err != nil {
		return nil, GitBoardProvenance{}, err
	}
	blob, err := readGit(ctx, path, dir, capBytes, "show", "--no-ext-diff", "--no-textconv", oid+":.mtix/tasks.json")
	if err != nil {
		return nil, GitBoardProvenance{}, err
	}
	if ctx.Err() != nil {
		return nil, GitBoardProvenance{}, gitBoardError("operation canceled", ctx.Err())
	}
	board, provenance, err := decodeGitBoard(blob)
	if err != nil {
		return nil, GitBoardProvenance{}, err
	}
	if ctx.Err() != nil {
		return nil, GitBoardProvenance{}, gitBoardError("operation canceled", ctx.Err())
	}
	provenance.Source = "git-merge-base"
	provenance.Candidates = [2]string{"ORIG_HEAD", "@{upstream}"}
	provenance.BaseOID = oid
	return board, provenance, nil
}

func boardOID(raw []byte) (string, error) {
	oid := strings.TrimSpace(string(raw))
	if len(oid) != 40 && len(oid) != 64 {
		return "", gitBoardError("invalid or ambiguous base", nil)
	}
	for _, c := range oid {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return "", gitBoardError("invalid or ambiguous base", nil)
		}
	}
	return oid, nil
}
