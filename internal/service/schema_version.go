// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// supportedSchemaVersion is the export schema version this build writes and
// the maximum major version it reads (FR-15.2g). 2.0.0 (MTIX-95.31.1) added
// annotations and the other node columns; 1.x files still import.
const supportedSchemaVersion = sqlite.SchemaVersionV1

// CheckSchemaVersion reports whether this build can read an export whose
// schema_version is fileVersion (FR-15.2g): nil when its major version is
// not higher than the one this build writes, an ErrInvalidInput error
// naming both versions otherwise. An empty version reads as 1.0.0.
// Auto-import and mtix import apply the same check (MTIX-95.31.1).
func CheckSchemaVersion(fileVersion string) error {
	if fileVersion == "" {
		fileVersion = "1.0.0"
	}
	if isSchemaCompatible(fileVersion) {
		return nil
	}
	return fmt.Errorf("schema version %s is newer than supported version %s \u2014 upgrade mtix: %w",
		fileVersion, supportedSchemaVersion, model.ErrInvalidInput)
}

// isSchemaCompatible checks if the file's schema version is compatible
// with this build per FR-15.2g. Compatible if major versions match.
func isSchemaCompatible(fileVersion string) bool {
	fileMajor := parseMajorVersion(fileVersion)
	supportedMajor := parseMajorVersion(supportedSchemaVersion)
	return fileMajor <= supportedMajor
}

// parseMajorVersion extracts the major version number from a semver string.
// Returns 1 for empty or unparsable versions (backward compatibility default).
func parseMajorVersion(version string) int {
	if version == "" {
		return 1
	}
	parts := strings.SplitN(version, ".", 2)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 1
	}
	return major
}
