// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Historical Git boards are decoded using pure existing import validation only.
package service

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func decodeGitBoard(raw []byte) (*sqlite.ExportData, GitBoardProvenance, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, GitBoardProvenance{}, gitBoardError("invalid board object", nil)
	}
	data, err := sqlite.DecodeExportData(bytes.NewReader(raw))
	if err != nil {
		return nil, GitBoardProvenance{}, gitBoardError("invalid board encoding", nil)
	}
	if err = CheckSchemaVersion(data.SchemaVersion); err != nil {
		return nil, GitBoardProvenance{}, gitBoardError("unsupported board schema", nil)
	}
	if err = sqlite.ValidateExport(data); err != nil {
		return nil, GitBoardProvenance{}, gitBoardError("invalid board content", nil)
	}
	form := boardForm(data)
	hash, err := exportHash(data, form)
	if err != nil {
		return nil, GitBoardProvenance{}, gitBoardError("invalid board hash", nil)
	}
	return data, GitBoardProvenance{Form: form.String(), BlobSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), ContentSHA256: hash}, nil
}

func boardForm(data *sqlite.ExportData) baselineForm {
	if parseMajorVersion(data.SchemaVersion) > 1 {
		return formCurrent
	}
	form := form100
	// Old files without uid fields remain without uids in their canonical form.
	for _, node := range data.Nodes {
		if node.UID != "" {
			return form
		}
	}
	return form | formWithoutUIDs
}
