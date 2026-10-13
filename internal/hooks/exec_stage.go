// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// stager writes verified private copies of approved project scripts.
// afterWrite is a test seam that runs once a copy is in place.
type stager struct {
	afterWrite func(path string)
}

// stage writes the script bytes already read into a content-named 0700
// directory and returns the copy's path after re-reading it.
func (st stager) stage(base string, s scriptInput) (string, error) {
	sum := sha256.Sum256(s.content)
	perm, kind := os.FileMode(0o600), "-r"
	if s.mode.Perm()&0o111 != 0 {
		perm, kind = 0o700, "-x"
	}
	// The mode is not part of the approval digest, so each mode gets its own
	// content-named directory and a chmod on the original reaches the copy.
	dir := filepath.Join(base, hex.EncodeToString(sum[:])+kind)
	for _, d := range []string{base, dir} {
		if err := privateDir(d); err != nil {
			return "", err
		}
	}
	target := filepath.Join(dir, filepath.Base(s.path))
	if sameBytes(target, s.content) {
		return target, nil
	}
	if err := writeCopy(dir, target, s.content, perm); err != nil {
		return "", fmt.Errorf("stage script %q: %w", filepath.Base(s.path), err)
	}
	if st.afterWrite != nil {
		st.afterWrite(target)
	}
	if !sameBytes(target, s.content) {
		return "", fmt.Errorf("stage script %q: copy does not match the approved bytes", filepath.Base(s.path))
	}
	return target, nil
}

// writeCopy writes content to a temporary file in dir and renames it onto
// target, removing the temporary file if any step fails.
func writeCopy(dir, target string, content []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, ".stage-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(content)
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Chmod(tmp.Name(), perm)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), target)
	}
	if err != nil {
		if rmErr := os.Remove(tmp.Name()); rmErr != nil && !os.IsNotExist(rmErr) {
			err = errors.Join(err, rmErr)
		}
	}
	return err
}

// privateDir creates dir with mode 0700 if absent and refuses a link or any
// other kind of entry.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("stage script: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("stage script: %q is not a directory", filepath.Base(dir))
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("stage script: %w", err)
	}
	return nil
}

func sameBytes(path string, want []byte) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	got, err := os.ReadFile(path) //nolint:gosec // reads back the private verified copy
	return err == nil && bytes.Equal(got, want)
}
