// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Hashes bind the received ordered export and explicit task-field presence.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func exchangeDigest(v string) bool {
	if len(v) != 64 || v != strings.ToLower(v) {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func optionalExchangeDigest(v string) bool { return v == "" || exchangeDigest(v) }
func receivedDigest(raw []byte) string     { h := sha256.Sum256(raw); return fmt.Sprintf("%x", h) }

func exchangeFields() []string {
	return strings.Fields("id parent_id depth seq project title description prompt acceptance node_type issue_type priority labels status progress assignee creator agent_state weight content_hash created_at updated_at closed_at defer_until deleted_at uid previous_status estimate_min actual_min code_refs commit_refs annotations invalidated_at invalidated_by invalidation_reason activity deleted_by metadata session_id")
}

func buildExchangeRecord(data *sqlite.ExportData, source, rawHash, stamp string) (*ExchangeRecord, error) {
	form := formCurrent
	tag := "current"
	owned := *data
	if data.SchemaVersion == "1.0.0" {
		old, err := sqlite.Schema100Form(data)
		if err != nil {
			return nil, exchangeUnavailable("historical-form", nil)
		}
		owned = *old
		form = form100
		tag = "schema-1.0.0"
	} else if data.SchemaVersion != "2.0.0" {
		return nil, exchangeUnavailable("unsupported-schema", nil)
	}
	digest, err := exportHash(data, form)
	if err != nil {
		return nil, exchangeUnavailable("board-hash", nil)
	}
	r := &ExchangeRecord{Format: 1, WrittenAt: stamp, Source: source, BoardForm: tag,
		BoardHash: digest, BoardFileHash: rawHash, NodeCount: len(owned.Nodes), Tasks: []ExchangeTask{}}
	for _, node := range owned.Nodes {
		raw, err := json.Marshal(node)
		if err != nil {
			return nil, exchangeUnavailable("task-json", nil)
		}
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, exchangeUnavailable("task-json", nil)
		}
		task := ExchangeTask{Kind: "legacy-id", Identity: node.ID, ID: node.ID, Hash: receivedDigest(raw), Fields: map[string]string{}}
		if node.UID != "" {
			task.Kind = "uid"
			task.Identity = node.UID
		}
		task.Fields = hashExchangeFields(fields)
		r.Tasks = append(r.Tasks, task)
	}
	sort.Slice(r.Tasks, func(i, j int) bool {
		return r.Tasks[i].Kind+":"+r.Tasks[i].Identity < r.Tasks[j].Kind+":"+r.Tasks[j].Identity
	})
	if err := validateExchangeTasks(r.Tasks); err != nil {
		return nil, err
	}
	return r, nil
}

func validateExchangeTasks(tasks []ExchangeTask) error {
	identities := map[string]bool{}
	ids := map[string]bool{}
	for _, task := range tasks {
		key := task.Kind + ":" + task.Identity
		if (task.Kind != "uid" && task.Kind != "legacy-id") || task.Identity == "" || task.ID == "" ||
			(task.Kind == "legacy-id" && task.Identity != task.ID) || identities[key] || ids[task.ID] || !exchangeDigest(task.Hash) {
			return exchangeUnavailable("task-identity", nil)
		}
		identities[key] = true
		ids[task.ID] = true
		if len(task.Fields) != len(exchangeFields()) {
			return exchangeUnavailable("task-fields", nil)
		}
		for _, key := range exchangeFields() {
			if !exchangeDigest(task.Fields[key]) {
				return exchangeUnavailable("task-fields", nil)
			}
		}
	}
	return nil
}

func hashExchangeFields(fields map[string]json.RawMessage) map[string]string {
	hashes := map[string]string{}
	for _, key := range exchangeFields() {
		value, ok := fields[key]
		field := []byte(`{"present":false}`)
		if ok {
			field = append(append([]byte(`{"present":true,"value":`), value...), '}')
		}
		hashes[key] = receivedDigest(field)
	}
	return hashes
}
