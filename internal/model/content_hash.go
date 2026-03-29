// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// contentHashPayload is the canonical structure for content hash computation per FR-3.7.
// Only content-relevant fields are included. State fields (status, priority),
// timestamps, metadata, activity, and computed fields (progress) are excluded.
type contentHashPayload struct {
	Acceptance  string   `json:"acceptance"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	Prompt      string   `json:"prompt"`
	Title       string   `json:"title"`
}

// ComputeContentHash computes the SHA256 content hash per FR-3.7.
// The hash is computed from the canonical JSON serialization of:
// title + description + prompt + acceptance + labels (sorted alphabetically).
//
// Keys are sorted (guaranteed by the struct field order matching alphabetical),
// no whitespace. State fields (status, priority) are excluded.
// Same content always produces the same hash.
func ComputeContentHash(title, description, prompt, acceptance string, labels []string) string {
	// Sort labels alphabetically for canonical ordering per FR-3.7.
	sortedLabels := make([]string, 0, len(labels))
	sortedLabels = append(sortedLabels, labels...)
	sort.Strings(sortedLabels)

	payload := contentHashPayload{
		Acceptance:  acceptance,
		Description: description,
		Labels:      sortedLabels,
		Prompt:      prompt,
		Title:       title,
	}

	// json.Marshal sorts keys by struct field order (which is alphabetical).
	// Compact JSON — no whitespace per FR-3.7.
	data, err := json.Marshal(payload)
	if err != nil {
		// This should never happen with string/[]string fields.
		// If it does, return a deterministic error hash.
		return fmt.Sprintf("error:%v", err)
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// ComputeHash computes the content hash for this Node per FR-3.7.
// Returns the SHA256 hex of canonical JSON of content fields only.
func (n *Node) ComputeHash() string {
	return ComputeContentHash(n.Title, n.Description, n.Prompt, n.Acceptance, n.Labels)
}
