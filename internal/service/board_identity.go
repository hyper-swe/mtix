// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
)

// CreateBoardID creates the committed identity once for a newly initialized board.
func CreateBoardID(mtixDir string) (string, error) {
	return createBoardID(mtixDir, uuid.NewRandom)
}

func createBoardID(mtixDir string, mint func() (uuid.UUID, error)) (string, error) {
	id, err := mint()
	if err != nil {
		return "", fmt.Errorf("create board identity: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(mtixDir, "board-id"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("create board identity: %w", err)
	}
	if err := writeBoardIdentity(file, id.String()); err != nil {
		return "", err
	}
	return id.String(), nil
}

// ReadBoardIDs reads a single committed identity or reports distinct identities in a conflict.
// It never creates or repairs the file.
func ReadBoardIDs(mtixDir string) ([]string, error) {
	path := filepath.Join(mtixDir, "board-id")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read board identity: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("board identity must be a regular file: %w", model.ErrInvalidInput)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open board identity: %w", err)
	}
	return readBoardIdentity(file)
}

func parseBoardIDs(raw []byte) ([]string, error) {
	if len(raw) > 4096 {
		return nil, fmt.Errorf("board identity is too large: %w", model.ErrInvalidInput)
	}
	var ids []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "<<<<<<< ") || line == "=======" || strings.HasPrefix(line, ">>>>>>> ") {
			continue
		}
		id, err := uuid.Parse(line)
		if err != nil || id == uuid.Nil || id.String() != line {
			return nil, fmt.Errorf("board identity must contain a canonical UUID: %w", model.ErrInvalidInput)
		}
		if !seen[line] {
			ids = append(ids, line)
			seen[line] = true
		}
	}
	if len(ids) > 1 {
		return ids, nil
	}
	if len(ids) != 1 || string(raw) != ids[0]+"\n" {
		return nil, fmt.Errorf("board identity must contain one UUID and newline: %w", model.ErrInvalidInput)
	}
	return ids, nil
}

type boardIdentityWriter interface {
	io.Writer
	Sync() error
	Close() error
}

func writeBoardIdentity(file boardIdentityWriter, id string) error {
	_, writeErr := io.WriteString(file, id+"\n")
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return fmt.Errorf("write board identity: %w", err)
	}
	return nil
}

func readBoardIdentity(file io.ReadCloser) ([]string, error) {
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, fmt.Errorf("read board identity: %w", err)
	}
	return parseBoardIDs(raw)
}
