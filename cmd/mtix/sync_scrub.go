// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// scrubSyncText is the central scrubber for sync command output
// (FR-18.17, MTIX-95.15). Every sync error print, every doctor detail,
// pg_dump's stderr and the CLI's final error line pass through it. It
// removes the hub DSN this process can resolve, exactly and with every
// password it carries, then masks any URL-shaped DSN (redact.Known). It
// never parses the DSN, so a DSN that does not parse is removed too.
func scrubSyncText(s string) string {
	return redact.Known(s, knownSyncDSNs()...)
}

// knownSyncDSNs returns the hub DSN values this process can resolve
// (FR-18.16, MTIX-95.15): the MTIX_SYNC_DSN value and the content of
// .mtix/secrets. Both are returned when both are set, so the scrub does
// not depend on which one transport.Source would pick, on the secrets
// file's mode, or on the DSN being valid.
func knownSyncDSNs() []string {
	var dsns []string
	if v := os.Getenv(transport.EnvDSN); v != "" {
		dsns = append(dsns, v)
	}
	if v, ok := readSecretsForScrub(scrubMtixDir()); ok {
		dsns = append(dsns, v)
	}
	return dsns
}

// scrubMtixDir returns the .mtix directory whose secrets file the
// scrubber reads (MTIX-95.15): the initialized app's, or, before app
// init (flag and argument errors), the one findMtixDir finds walking up
// from the working directory. It returns "" outside any project, where
// there is no secrets file to read.
func scrubMtixDir() string {
	if app.mtixDir != "" {
		return app.mtixDir
	}
	dir, err := findMtixDir()
	if err != nil {
		return ""
	}
	return dir
}

// readSecretsForScrub returns the content of mtixDir's secrets file for
// the scrubber, and whether there is one (MTIX-95.15). It reads through
// transport.ReadSecretsFile, the reader transport.Source uses, so the
// scrubber knows exactly the secrets-file DSN that resolution would use
// (a symlink is followed; the target must be a regular file of at most
// 64 KiB). The file's mode is not checked: a DSN in a file Source would
// refuse is still removed. An absent or unreadable file gives nothing.
func readSecretsForScrub(mtixDir string) (string, bool) {
	if mtixDir == "" {
		return "", false
	}
	body, _, err := transport.ReadSecretsFile(mtixDir)
	if err != nil {
		return "", false
	}
	return body, true
}

// warnSync writes one warning line, "prefix: error text", with the error
// text passed through the central scrubber (FR-18.17, MTIX-95.15). The
// sync warnings that do not stop a command use it.
func warnSync(w io.Writer, prefix string, err error) {
	fmt.Fprintf(w, "%s: %s\n", prefix, scrubSyncText(err.Error()))
}

// scrubDoctorReport returns r with every check detail and fix passed
// through scrubSyncText, so neither the text nor the --json report of mtix
// sync doctor carries a DSN or its password (FR-18.17, MTIX-95.15,
// MTIX-95.7).
func scrubDoctorReport(r DoctorReport) DoctorReport {
	checks := make([]DoctorCheck, len(r.Checks))
	for i, c := range r.Checks {
		c.Detail = scrubSyncText(c.Detail)
		c.Fix = scrubSyncText(c.Fix)
		checks[i] = c
	}
	r.Checks = checks
	return r
}

// scrubWriter passes each line written to it through the central
// scrubber before writing it on (FR-18.17, MTIX-95.15). It holds a
// partial line until its newline arrives, so a secret split across
// writes is still removed; Flush writes a final line that has no
// newline. The known DSNs are read once, when the writer is made.
type scrubWriter struct {
	w     io.Writer
	known []string
	buf   []byte
}

// newScrubWriter returns a scrubWriter that writes to w.
func newScrubWriter(w io.Writer) *scrubWriter {
	return &scrubWriter{w: w, known: knownSyncDSNs()}
}

// Write buffers p and writes every complete line, scrubbed. It reports
// len(p), or 0 and the error when writing to the underlying writer fails.
func (s *scrubWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := s.buf[:i+1]
		s.buf = s.buf[i+1:]
		if err := s.emit(line); err != nil {
			return 0, err
		}
	}
}

// Flush writes the buffered partial line, scrubbed.
func (s *scrubWriter) Flush() error {
	line := s.buf
	s.buf = nil
	return s.emit(line)
}

// emit writes one scrubbed line; an empty line writes nothing.
func (s *scrubWriter) emit(line []byte) error {
	if len(line) == 0 {
		return nil
	}
	if _, err := io.WriteString(s.w, redact.Known(string(line), s.known...)); err != nil {
		return fmt.Errorf("write scrubbed output: %w", err)
	}
	return nil
}

// syncExactArgs is the positional-argument rule for a sync or daemon
// command whose only positional arguments are the n it documents
// (FR-18.16, MTIX-95.15). Anything past them is refused by
// refuseDSNArgs, the rule resolveSyncDSN applies, before the command
// runs. Unlike cobra.NoArgs and cobra.ExactArgs, neither refusal
// repeats an argument, which may be a DSN typed in the wrong place.
func syncExactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return fmt.Errorf("%s: %w", cmd.CommandPath(), refuseDSNArgs(args[n:]))
		}
		if len(args) < n {
			return fmt.Errorf("%s: requires %d argument(s), received %d", cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}
