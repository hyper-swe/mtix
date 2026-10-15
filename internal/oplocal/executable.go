// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"regexp"
)

// ErrUnsupportedPlatform means the operation does not exist on this platform.
var ErrUnsupportedPlatform = errors.New("not supported on this platform")

var executableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// executableName validates the name of a release-defined executable. Names
// have no extension, so they never collide with a JSON record or host-id.
func executableName(name string) (string, error) {
	if !executableNamePattern.MatchString(name) || name == "host-id" {
		return "", invalid("executable name is invalid")
	}
	return name, nil
}
