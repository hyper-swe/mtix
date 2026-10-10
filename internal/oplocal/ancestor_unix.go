//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"golang.org/x/sys/unix"
	"math"
	"os"
	"path/filepath"
)

func verifyAncestor(dir *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(checkedFD(dir), &stat); err != nil {
		return failure("ancestor metadata is unavailable", err)
	}
	return validateAncestor(stat.Uid, uint32(stat.Mode))
}
func validateAncestor(uid, mode uint32) error {
	effective := os.Geteuid()
	if effective < 0 || effective > math.MaxUint32 {
		return invalid("user identity is invalid")
	}
	if (uid != 0 && uid != uint32(effective)) || mode&unix.S_IFMT != unix.S_IFDIR || mode&0022 != 0 {
		return invalid("ancestor owner, permissions or type is invalid")
	}
	return nil
}

func checkUnixLocation(target string, roots []string) error {
	if !filepath.IsAbs(target) {
		return invalid("directory must be absolute")
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		canonical, err := canonicalLocation(root)
		if err != nil {
			return err
		}
		if inside("linux", target, canonical) {
			return invalid("directory must be outside " + root)
		}
	}
	return nil
}

func canonicalExistingPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
