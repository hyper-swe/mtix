// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package docs embeds documentation templates into the binary per FR-13.1.
package docs

import "embed"

//go:embed templates/*.tmpl templates/skills/*.tmpl templates/skills/references/*.tmpl
var embeddedTemplates embed.FS
