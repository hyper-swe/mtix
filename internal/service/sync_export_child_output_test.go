// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

const childOutputHalfLimit = 16 * 1024

// childDiagnosticOutput retains a bounded head and tail, including late errors.
// Its sole writer is joined before String is called.
type childDiagnosticOutput struct {
	head, tail []byte
	total      int64
}

func (b *childDiagnosticOutput) Write(p []byte) (int, error) {
	n := len(p)
	b.appendBytes(p)
	return n, nil
}

func (b *childDiagnosticOutput) appendBytes(p []byte) {
	b.total += int64(len(p))
	if remaining := childOutputHalfLimit - len(b.head); remaining > 0 {
		n := min(remaining, len(p))
		b.head = append(b.head, p[:n]...)
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	if len(p) >= childOutputHalfLimit {
		b.tail = append(b.tail[:0], p[len(p)-childOutputHalfLimit:]...)
		return
	}
	if discard := len(b.tail) + len(p) - childOutputHalfLimit; discard > 0 {
		copy(b.tail, b.tail[discard:])
		b.tail = b.tail[:len(b.tail)-discard]
	}
	b.tail = append(b.tail, p...)
}

func (b *childDiagnosticOutput) String() string {
	omitted := b.total - int64(len(b.head)+len(b.tail))
	if omitted > 0 {
		return string(b.head) + fmt.Sprintf("\n[truncated %d bytes]\n", omitted) + string(b.tail)
	}
	return string(b.head) + string(b.tail)
}

// readOutput drains arbitrarily long lines in bounded chunks. Only complete
// short protocol lines queue; all output is retained through the bounded writer.
func (c *coalescingWriter) readOutput(stdout io.Reader) error {
	reader := bufio.NewReader(stdout)
	var line []byte
	oversized := false
	for {
		part, err := reader.ReadSlice('\n')
		c.diagnostics.appendBytes(part)
		if !oversized {
			if len(line)+len(part) > 64*1024 {
				oversized = true
				line = line[:0]
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !oversized && len(line) > 0 {
			text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
			if coalescingProtocolLine(text) {
				c.lines <- text
			}
		}
		line = line[:0]
		oversized = false
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read child stdout: %w", err)
		}
	}
}

func coalescingProtocolLine(line string) bool {
	return line == "ready" || line == "marked 32" || line == "snapshot-held" ||
		line == "committed 2" || line == "writer-held" || strings.HasPrefix(line, "force-result ")
}
