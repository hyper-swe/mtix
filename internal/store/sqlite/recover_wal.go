// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// checkRecoverWAL checks the log before SQLite can silently ignore a bad suffix
// (FR-19.1, MTIX-112.48). This parses WAL headers/checksums, never btree pages.
func checkRecoverWAL(ctx context.Context, path string) (err error) {
	// Inspect the path before opening: a FIFO open waits for a writer and
	// cannot be interrupted by the recovery context (FR-19.1).
	info, err := os.Lstat(path + "-wal") // #nosec G703 -- Recovery intentionally reads the caller-selected database sidecar; there is no confined root.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat WAL path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("WAL is not a regular file")
	}
	f, err := os.Open(path + "-wal") // #nosec G703 -- Recovery intentionally reads the caller-selected database sidecar; there is no confined root.
	if err != nil {
		return fmt.Errorf("open WAL: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	before, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat WAL: %w", err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("WAL is not a regular file")
	}
	if before.Size() == 0 {
		return nil
	}
	if walErr := verifyRecoverWAL(ctx, io.NewSectionReader(f, 0, before.Size()), before.Size()); walErr != nil {
		return walErr
	}
	after, err := os.Stat(path + "-wal") // #nosec G703 -- Recovery intentionally reads the caller-selected database sidecar; there is no confined root.
	if err != nil {
		return fmt.Errorf("recheck WAL: %w", err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("WAL changed during verification")
	}
	return nil
}

type recoverWALHeader struct {
	pageSize int
	order    binary.ByteOrder
	salts    []byte
	sum      [2]uint32
}

// SQLite's persistent WAL format: https://www.sqlite.org/fileformat2.html#walformat.
func readRecoverWALHeader(r io.Reader) (recoverWALHeader, error) {
	data := make([]byte, 32)
	if _, err := io.ReadFull(r, data); err != nil {
		return recoverWALHeader{}, fmt.Errorf("read WAL header: %w", err)
	}
	h := recoverWALHeader{order: binary.LittleEndian, salts: data[16:24]}
	switch binary.BigEndian.Uint32(data[:4]) {
	case 0x377f0682:
	case 0x377f0683:
		h.order = binary.BigEndian
	default:
		return h, fmt.Errorf("WAL header magic cannot be verified")
	}
	if binary.BigEndian.Uint32(data[4:8]) != 3007000 {
		return h, fmt.Errorf("WAL header version cannot be verified")
	}
	h.pageSize = int(binary.BigEndian.Uint32(data[8:12]))
	if h.pageSize < 512 || h.pageSize > 65536 || h.pageSize&(h.pageSize-1) != 0 {
		return h, fmt.Errorf("WAL page size cannot be verified")
	}
	h.sum = recoverWALChecksum(h.order, data[:24], [2]uint32{})
	if h.sum[0] != binary.BigEndian.Uint32(data[24:28]) || h.sum[1] != binary.BigEndian.Uint32(data[28:32]) {
		return h, fmt.Errorf("WAL header checksum does not match")
	}
	return h, nil
}

// verifyRecoverWAL treats even stale reset tails as unproven (lead ruling,
// MTIX-112.48): a changed salt cannot authenticate itself as harmless history.
func verifyRecoverWAL(ctx context.Context, r io.Reader, size int64) error {
	h, err := readRecoverWALHeader(r)
	if err != nil {
		return err
	}
	frameSize := int64(24 + h.pageSize)
	if (size-32)%frameSize != 0 {
		return fmt.Errorf("WAL tail does not end on a frame boundary")
	}
	frame := make([]byte, frameSize)
	for i := int64(0); i < (size-32)/frameSize; i++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("WAL check did not finish: %w", err)
		}
		if _, err := io.ReadFull(r, frame); err != nil {
			return fmt.Errorf("read WAL frame %d: %w", i, err)
		}
		if !bytes.Equal(frame[8:16], h.salts) {
			return fmt.Errorf("WAL tail salt cannot be verified at frame %d", i)
		}
		h.sum = recoverWALChecksum(h.order, frame[:8], h.sum)
		h.sum = recoverWALChecksum(h.order, frame[24:], h.sum)
		if h.sum[0] != binary.BigEndian.Uint32(frame[16:20]) || h.sum[1] != binary.BigEndian.Uint32(frame[20:24]) {
			return fmt.Errorf("WAL frame %d checksum does not match", i)
		}
		if binary.BigEndian.Uint32(frame[:4]) == 0 {
			return fmt.Errorf("WAL frame %d has no page number", i)
		}
	}
	return nil
}

// recoverWALChecksum follows SQLite's rolling two-word checksum. Values are
// stored big-endian, but the input word order is selected by the WAL magic.
func recoverWALChecksum(order binary.ByteOrder, data []byte, sum [2]uint32) [2]uint32 {
	for i := 0; i < len(data); i += 8 {
		sum[0] += order.Uint32(data[i:i+4]) + sum[1]
		sum[1] += order.Uint32(data[i+4:i+8]) + sum[0]
	}
	return sum
}
