// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// mergeNodeActivity is a read-only projection of activity and annotations per FR-3.6.
// An annotation is authoritative over an older activity copy with the same ID.
func mergeNodeActivity(activity []model.ActivityEntry, annotations []model.Annotation) []model.ActivityEntry {
	entries := make([]model.ActivityEntry, 0, len(activity)+len(annotations))
	positions := make(map[string]int, cap(entries))
	put := func(entry model.ActivityEntry) {
		if entry.ID != "" {
			if index, exists := positions[entry.ID]; exists {
				entries[index] = entry
				return
			}
			positions[entry.ID] = len(entries)
		}
		entries = append(entries, entry)
	}
	for _, entry := range activity {
		put(entry)
	}
	for _, annotation := range annotations {
		put(model.ActivityEntry{
			ID: annotation.ID, Type: model.ActivityTypeComment,
			Author: annotation.Author, Addressee: annotation.Addressee,
			Text: annotation.Text, CreatedAt: annotation.CreatedAt,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].ID > entries[j].ID
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	return entries
}

// activityPage applies FR-3.6a bounds without overflowing offset plus limit.
func activityPage(entries []model.ActivityEntry, limit, offset int) []model.ActivityEntry {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(entries) {
		return []model.ActivityEntry{}
	}
	entries = entries[offset:]
	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}
	return entries
}
