// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lease outcomes use checked plain and JSON output.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func writeLeaseOutput(_ *cobra.Command, jsonOutput bool, value any, plain string) error {
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			return fmt.Errorf("write lease JSON: %w", err)
		}
		return nil
	}
	if _, err := fmt.Fprintln(os.Stdout, plain); err != nil {
		return fmt.Errorf("write lease outcome: %w", err)
	}
	return nil
}
