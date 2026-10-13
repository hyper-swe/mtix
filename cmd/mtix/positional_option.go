// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
)

// positionalOrFlag resolves a value that may be given as the single optional
// positional argument or as a flag (MTIX-146: the documented forms
// `mtix init <prefix>` and `mtix plugin install <target>`). When both are
// given and differ, the request is ambiguous and is refused with
// ErrInvalidInput; when only the positional is given, it wins over the flag
// default. flagValue is the current value of the flag named flagName.
func positionalOrFlag(cmd *cobra.Command, flagName, flagValue string, args []string) (string, error) {
	if len(args) == 0 {
		return flagValue, nil
	}
	if cmd.Flags().Changed(flagName) && flagValue != args[0] {
		return "", fmt.Errorf("%s given as argument %q and as --%s %q: %w",
			flagName, args[0], flagName, flagValue, model.ErrInvalidInput)
	}
	return args[0], nil
}
