// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import _ "embed"

// piExtension is the user-level pi extension (MTIX-112.19.7). It is EXPERIMENTAL:
// the live pi leg (interactive extension trust and a real-harness wake) is not proven,
// so no default path installs it.
//
//go:embed assets/pi/mtix-attach.ts
var piExtension []byte

// PiExtensionSource returns a copy of the embedded pi extension. No refresh, setup,
// doctor or docs path calls it yet: nothing writes the asset to disk by default
// (trunk rule 1, lead ruling 2026-11-01).
func PiExtensionSource() []byte {
	return append([]byte(nil), piExtension...)
}
