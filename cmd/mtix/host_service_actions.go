// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

type hostServiceRunner func(context.Context, []string) ([]byte, error)

func applyHostServiceAction(cmd *cobra.Command, spec *serviceSpec, verb string, run hostServiceRunner) error {
	if verb == "install" {
		if err := prepareHostInstall(cmd, spec); err != nil {
			return errors.Join(err, finishHostServiceAction("uninstall", spec))
		}
	}
	err := runHostServiceCommands(cmd.Context(), cmd.OutOrStdout(), hostServiceArguments(spec, verb), run)
	if verb == "uninstall" {
		err = errors.Join(err, finishHostServiceAction(verb, spec))
	} else if err == nil {
		err = finishHostServiceAction(verb, spec)
	}
	if verb == "install" && err != nil {
		rollbackErr := runHostServiceCommands(cmd.Context(), cmd.OutOrStdout(), spec.Unregister, run)
		return fmt.Errorf("dispatcher install: %w", errors.Join(err, rollbackErr, finishHostServiceAction("uninstall", spec)))
	}

	if err != nil {
		return fmt.Errorf("dispatcher %s: %w", verb, err)
	}
	return nil
}

func runHostServiceCommand(ctx context.Context, argv []string) ([]byte, error) {
	return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput() // #nosec G204 -- argv comes only from the native service specification
}

func runHostServiceCommands(ctx context.Context, out io.Writer, cmds [][]string, run hostServiceRunner) error {
	for _, argv := range cmds {
		fmt.Fprintf(out, "$ %s\n", strings.Join(argv, " "))
		output, err := run(ctx, argv)
		if len(output) > 0 {
			fmt.Fprint(out, string(output))
		}
		if err != nil && !hostServiceAlreadyAbsent(argv, string(output)) {
			return fmt.Errorf("%s: %s: %w", argv[0], strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}

// Missing jobs are idempotent only for removal/stop, never for register or status.
func hostServiceAlreadyAbsent(argv []string, output string) bool {
	text := strings.ToLower(output)
	args := strings.Join(argv[1:], " ")
	switch argv[0] {
	case "launchctl":
		return strings.HasPrefix(args, "bootout ") && (strings.Contains(text, "no such process") || strings.Contains(text, "could not find service") || strings.Contains(text, "no such file or directory"))
	case "systemctl":
		removing := strings.Contains(args, "disable ") || strings.Contains(args, "stop ")
		return removing && (strings.Contains(text, "does not exist") || strings.Contains(text, "not loaded") || strings.Contains(text, "no such file or directory"))
	case "schtasks":
		removing := strings.HasPrefix(args, "/Delete ") || strings.HasPrefix(args, "/End ")
		return removing && (strings.Contains(text, "cannot find the file specified") || strings.Contains(text, "not currently running"))
	}
	return false
}
