// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"sort"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// nodeUIDs maps the node ids of a board or a store to their uids ("" when
// a node has none), for Compare (MTIX-95.31.4).
type nodeUIDs map[string]string

// fileNodeUIDs reads the node ids and uids of tasks.json content, and how
// many nodes it lists. Nodes without an id are counted but not mapped.
func fileNodeUIDs(fileBytes []byte) (nodeUIDs, int, error) {
	var fileData struct {
		Nodes []struct {
			ID  string `json:"id"`
			UID string `json:"uid"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(fileBytes, &fileData); err != nil {
		return nil, 0, err
	}
	uids := make(nodeUIDs, len(fileData.Nodes))
	for _, n := range fileData.Nodes {
		if n.ID != "" {
			uids[n.ID] = n.UID
		}
	}
	return uids, len(fileData.Nodes), nil
}

// exportNodeUIDs maps the node ids of a store's export to their uids.
func exportNodeUIDs(data *sqlite.ExportData) nodeUIDs {
	uids := make(nodeUIDs, len(data.Nodes))
	for i := range data.Nodes {
		if id := data.Nodes[i].ID; id != "" {
			uids[id] = data.Nodes[i].UID
		}
	}
	return uids
}

// ids returns the set of mapped ids.
func (m nodeUIDs) ids() map[string]bool {
	ids := make(map[string]bool, len(m))
	for id := range m {
		ids[id] = true
	}
	return ids
}

// differentUIDs returns, sorted, the ids that both hold under different
// non-empty uids.
func differentUIDs(file, db nodeUIDs) []string {
	var ids []string
	for id, fileUID := range file {
		if dbUID, held := db[id]; held && fileUID != "" && dbUID != "" && fileUID != dbUID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
