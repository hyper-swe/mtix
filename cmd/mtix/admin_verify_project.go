// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store"
)

// runVerifyProject checks every node's content hash and that no two nodes
// share a uid (MTIX-95.31.8), printing the result as text or JSON.
func runVerifyProject(ctx context.Context) error {
	nodes, _, err := app.store.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 10000})
	if err != nil {
		return fmt.Errorf("list nodes for verification: %w", err)
	}

	var mismatches []string
	for _, n := range nodes {
		expected := n.ComputeHash()
		if expected != n.ContentHash {
			mismatches = append(mismatches, n.ID)
		}
	}

	// MTIX-95.31.8: no two nodes may share a non-empty uid.
	uidReport, err := app.store.DuplicateNodeUIDsReport(ctx)
	if err != nil {
		return fmt.Errorf("check node uids for verification: %w", err)
	}

	if app.jsonOutput {
		out := map[string]any{
			"total_nodes":   len(nodes),
			"verified":      len(mismatches) == 0 && uidReport == "",
			"mismatches":    mismatches,
			"uid_unique_ok": uidReport == "",
		}
		if uidReport != "" {
			out["uid_unique_recovery"] = uidReport
		}
		data, err := json.Marshal(out)
		if err != nil {
			return fmt.Errorf("encode verify result: %w", err)
		}
		fmt.Println(string(data))
	} else {

		fmt.Printf("Verified %d nodes\n", len(nodes))
		if len(mismatches) > 0 {
			fmt.Printf("INTEGRITY FAILURE: %d nodes with hash mismatches:\n", len(mismatches))
			for _, id := range mismatches {
				fmt.Printf("  - %s\n", id)
			}
		} else {
			fmt.Println("All content hashes verified OK")
		}
		if uidReport != "" {
			fmt.Printf("INTEGRITY FAILURE: %s\n", uidReport)
		} else {
			fmt.Println("All node uids unique")
		}
	}
	return nil
}
