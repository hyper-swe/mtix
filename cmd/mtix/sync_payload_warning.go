// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Mutation-time warning of the CLI for sync events over the wire cap
// (MTIX-95.12). When a hub is configured, every mutation command runs with a
// context carrying app.payloadWarnings (mutationContext), so emitEvent
// reports an event whose payload is over the 64 KB cap; after the command
// succeeds the root command prints one warning line per such event on
// stderr (printPayloadWarnings), naming the field and the limit. The
// mutation itself succeeds; `mtix sync push` holds the event.

// hubConfigured reports whether the project in mtixDir has a sync hub
// configured: a DSN in MTIX_SYNC_DSN or .mtix/secrets (transport.Source).
// A DSN that transport.Source refuses to use (a secrets file with the wrong
// mode, a DSN in a tracked config file) still counts: a hub was set up, and
// the events will be pushed once the DSN is fixed. It reads no secret value
// beyond what transport.Source reads and never prints one.
func hubConfigured(mtixDir string) bool {
	if mtixDir == "" {
		return false
	}
	_, err := transport.Source(mtixDir)
	return err == nil || !errors.Is(err, transport.ErrDSNNotConfigured)
}

// newPayloadWarnings returns the collector of this process's wire-cap
// warnings, or nil when no hub is configured: without a hub no event is
// pushed, so none can be held.
func newPayloadWarnings(mtixDir string) *sqlite.PayloadWarnings {
	if !hubConfigured(mtixDir) {
		return nil
	}
	return &sqlite.PayloadWarnings{}
}

// mutationContext returns the context of a CLI mutation: a background
// context that carries app.payloadWarnings when a hub is configured.
func mutationContext() context.Context {
	ctx := context.Background()
	if app.payloadWarnings != nil {
		ctx = sqlite.WithPayloadWarnings(ctx, app.payloadWarnings)
	}
	return ctx
}

// printPayloadWarnings writes each collected wire-cap warning to w on its
// own line and empties the collector. The root command calls it after a
// command succeeds, so a mutation that failed and rolled back warns about
// nothing.
func printPayloadWarnings(w io.Writer) {
	if app.payloadWarnings == nil {
		return
	}
	for _, pw := range app.payloadWarnings.Drain() {
		fmt.Fprintln(w, pw.String())
	}
}

// setProjectDir records the project's .mtix directory for this process and,
// when a hub is configured, starts its wire-cap warning collector. initApp
// calls it once the store and services are wired.
func setProjectDir(mtixDir string) {
	app.mtixDir = mtixDir
	app.payloadWarnings = newPayloadWarnings(mtixDir)
}

// finishCommand ends a command that succeeded: it prints the collected
// wire-cap warnings to the command's stderr, then closes the app. The root
// command's post-run calls it; a failed command skips it, so a mutation
// that rolled back warns about nothing.
func finishCommand(cmd *cobra.Command) error {
	printPayloadWarnings(cmd.ErrOrStderr())
	return closeApp()
}
