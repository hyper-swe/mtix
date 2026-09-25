// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// isStatusField reports whether a node field (by its export key) describes
// the node's status: a merge keeps these together (keepLocalBlankedFields).
func isStatusField(key string) bool {
	switch key {
	case "status", "previous_status", "progress", "closed_at", "defer_until", "assignee",
		"agent_state", "invalidated_at", "invalidated_by", "invalidation_reason":
		return true
	}
	return false
}

// isContentField reports whether a node field (by its export key) is part
// of the content hash (FR-3.7).
func isContentField(key string) bool {
	switch key {
	case "title", "description", "prompt", "acceptance", "labels":
		return true
	}
	return false
}

// keepLocalBlankedFields makes a merge keep, in merged (the file's copy of a
// node that is not current, copyIsCurrent), every non-empty local field
// value the copy leaves empty: the field losses DiffReplace lists
// (blankedFields), so the merge an auto-import refusal recommends keeps them
// (MTIX-95.31.4, FR-15.2i). When one of them describes the node's status (a
// closed time, an assignee, a wake time), every status field keeps its
// local value, so the node never takes the copy's status with the local
// closed time. When one of them is content, the content hash is recomputed
// over the merged content (FR-3.7). The copy's other values stand.
func keepLocalBlankedFields(merged, local *exportNode) error {
	localJSON, err := json.Marshal(local)
	if err != nil {
		return fmt.Errorf("encode local node %s: %w", local.ID, err)
	}
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encode node %s of the file: %w", merged.ID, err)
	}
	keys, err := blankedFields(localJSON, mergedJSON)
	if err != nil || len(keys) == 0 {
		return err
	}
	var localFields, fields map[string]json.RawMessage
	if err := json.Unmarshal(localJSON, &localFields); err != nil {
		return fmt.Errorf("decode local node %s: %w", local.ID, err)
	}
	if err := json.Unmarshal(mergedJSON, &fields); err != nil {
		return fmt.Errorf("decode node %s of the file: %w", merged.ID, err)
	}
	keepStatus, rehash := false, false
	for _, key := range keys {
		fields[key] = localFields[key]
		keepStatus = keepStatus || isStatusField(key)
		rehash = rehash || isContentField(key)
	}
	if keepStatus {
		keepLocalStatusFields(fields, localFields)
	}
	return rebuildMerged(merged, fields, rehash)
}

// keepLocalStatusFields sets every status field of fields to its local
// value, leaving out the ones the local node does not set.
func keepLocalStatusFields(fields, localFields map[string]json.RawMessage) {
	for key := range fields {
		if _, held := localFields[key]; isStatusField(key) && !held {
			delete(fields, key)
		}
	}
	for key, value := range localFields {
		if isStatusField(key) {
			fields[key] = value
		}
	}
}

// rebuildMerged decodes fields into merged and, when rehash is set,
// recomputes its content hash over its title, description, prompt,
// acceptance and labels (FR-3.7).
func rebuildMerged(merged *exportNode, fields map[string]json.RawMessage, rehash bool) error {
	raw, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("encode merged node %s: %w", merged.ID, err)
	}
	var out exportNode
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("decode merged node %s: %w", merged.ID, err)
	}
	if rehash {
		var labels []string
		if out.Labels != "" {
			if err := json.Unmarshal([]byte(out.Labels), &labels); err != nil {
				return fmt.Errorf("read the labels of node %s: %w", out.ID, err)
			}
		}
		out.ContentHash = model.ComputeContentHash(out.Title, out.Description, out.Prompt, out.Acceptance, labels)
	}
	*merged = out
	return nil
}

// mergeUnchangedContent writes a merged node whose content hash the file
// leaves unchanged (FR-7.8): the local field values stand. The merged
// annotations and activity are written when they grew, and the file's uid
// is adopted when the file holds the same task under another uid
// (MTIX-95.31.4, differentIdentity), as for a node whose content changed.
func mergeUnchangedContent(ctx context.Context, tx *sql.Tx, merged *exportNode, localUID string,
	streamsChanged bool) (importAction, error) {
	adoptUID := merged.UID != "" && merged.UID != localUID
	if !streamsChanged && !adoptUID {
		return importActionSkipped, nil
	}
	if streamsChanged {
		if err := writeNodeStreams(ctx, tx, merged); err != nil {
			return 0, fmt.Errorf("merge annotations and activity of node %s: %w", merged.ID, err)
		}
	}
	if adoptUID {
		// The same task under another uid: take the file's uid.
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, merged.UID, merged.ID); err != nil {
			return 0, fmt.Errorf("adopt the file's uid for node %s: %w", merged.ID, err)
		}
	}
	return importActionUpdated, nil
}
