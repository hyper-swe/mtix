// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package store

// HistoryReaderCapability is the marker every session registration and
// dispatcher heartbeat written by a build that keeps and reads task history
// carries in its capabilities column (MTIX-95.31.19.6). The history.export
// guard judges a signal by this marker, never by its version string: released
// 0.5.4 prints dev and builds from main stamp git-describe versions.
const HistoryReaderCapability = "history_export_reader"

// BinarySignal is one record of an mtix build that has touched a board: a
// session registration or a dispatcher (daemon) heartbeat, with the version
// that wrote it and when it was last seen. It carries no board content.
type BinarySignal struct {
	Source  string `json:"source"` // "registration" or "daemon"
	Name    string `json:"name"`   // agent id, or host:pid for a daemon
	Version string `json:"version"`
	// Capabilities is the comma-separated marker list the writer recorded;
	// empty for a row written by a build that predates the markers.
	Capabilities string `json:"capabilities,omitempty"`
	SeenAt       string `json:"seen_at"`
}
