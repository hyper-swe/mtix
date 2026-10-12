// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxApprovalIndex = 16 << 20

func legacyTracked(root string) (bool, string) {
	git, relative, err := approvalGitLocation(root)
	if err != nil {
		return true, "git location cannot be verified; review hooks individually"
	}
	if git == "" {
		return false, "workspace is not git tracked"
	}
	info, err := os.Lstat(git)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true, "git location cannot be verified; review hooks individually"
	}
	path := filepath.Join(git, "index")
	info, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return true, "git index is absent; review hooks individually"
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return true, "git index cannot be verified; review hooks individually"
	}
	data, err := readIndex(path)
	if err != nil {
		return true, "git index cannot be read; review hooks individually"
	}
	tracked, err := indexContainsProjectApproval(data, relative)
	if err != nil {
		return true, "git index requires individual hook review: " + err.Error()
	}
	if tracked {
		return true, "tracked legacy workspace record is ignored"
	}
	return false, "legacy workspace record is untracked"
}

func readIndex(path string) (data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = fmt.Errorf("close git index: %w", closeErr)
		}
	}()
	data, err = io.ReadAll(io.LimitReader(f, maxApprovalIndex+1))
	if err == nil && len(data) > maxApprovalIndex {
		err = fmt.Errorf("index exceeds size limit")
	}
	return data, err
}

func indexContainsApproval(data []byte) (bool, error) {
	return indexContainsProjectApproval(data, ".mtix/hooks.trust")
}

func indexContainsProjectApproval(data []byte, relative string) (bool, error) {
	if len(data) < 32 || string(data[:4]) != "DIRC" {
		return false, fmt.Errorf("invalid index header")
	}
	version := binary.BigEndian.Uint32(data[4:8])
	if version < 2 || version > 4 {
		return false, fmt.Errorf("unsupported index version")
	}
	count := binary.BigEndian.Uint32(data[8:12])
	if count > 200000 {
		return false, fmt.Errorf("index entry limit exceeded")
	}
	body := data[:len(data)-20]
	checksum := sha1.Sum(body) //nolint:gosec // This validates the Git index format checksum.
	if !bytes.Equal(checksum[:], data[len(data)-20:]) {
		return false, fmt.Errorf("index checksum differs")
	}
	pos, previous, tracked := 12, "", false
	for i := uint32(0); i < count; i++ {
		name, next, err := indexEntry(body, pos, version, previous)
		if err != nil {
			return false, err
		}
		if strings.EqualFold(name, relative) || strings.HasPrefix(strings.ToLower(relative), strings.ToLower(strings.TrimSuffix(name, "/"))+"/") {
			tracked = true
		}
		pos, previous = next, name
	}
	if err := approvalIndexExtensions(body[pos:]); err != nil {
		return false, err
	}
	return tracked, nil
}

func indexEntry(data []byte, start int, version uint32, previous string) (string, int, error) {
	if start > len(data)-62 {
		return "", 0, fmt.Errorf("incomplete index entry")
	}
	flags := binary.BigEndian.Uint16(data[start+60 : start+62])
	pos := start + 62
	if flags&0x4000 != 0 {
		if version == 2 || pos > len(data)-2 {
			return "", 0, fmt.Errorf("invalid extended entry")
		}
		pos += 2
	}
	prefix := ""
	if version == 4 {
		remove, next, err := indexPrefix(data, pos)
		if err != nil || remove > len(previous) {
			return "", 0, fmt.Errorf("invalid index prefix")
		}
		prefix, pos = previous[:len(previous)-remove], next
	}
	end := bytes.IndexByte(data[pos:], 0)
	if end < 0 {
		return "", 0, fmt.Errorf("incomplete index name")
	}
	name := prefix + string(data[pos:pos+end])
	if name == "" || len(name) > 4096 || strings.HasPrefix(name, "/") {
		return "", 0, fmt.Errorf("invalid index name")
	}
	pos += end + 1
	if version != 4 {
		pos = start + ((pos-start+7)/8)*8
	}
	if pos > len(data) {
		return "", 0, fmt.Errorf("incomplete index padding")
	}
	return name, pos, nil
}

func indexPrefix(data []byte, pos int) (int, int, error) {
	value := 0
	for n := 0; n < 5 && pos < len(data); n++ {
		current := data[pos]
		pos++
		value = value<<7 | int(current&0x7f)
		if value > 4096 {
			return 0, 0, fmt.Errorf("index prefix exceeds limit")
		}
		if current&0x80 == 0 {
			return value, pos, nil
		}
		value++
	}
	return 0, 0, fmt.Errorf("incomplete index prefix")
}

func approvalIndexExtensions(data []byte) error {
	for len(data) > 0 {
		if len(data) < 8 {
			return fmt.Errorf("incomplete index extension")
		}
		size := binary.BigEndian.Uint32(data[4:8])
		if int64(size) > int64(len(data)-8) {
			return fmt.Errorf("incomplete extension data")
		}
		switch string(data[:4]) {
		case "TREE", "REUC", "EOIE", "IEOT":
		default:
			return fmt.Errorf("unsupported index extension")
		}
		data = data[8+int(size):]
	}
	return nil
}

func approvalGitLocation(root string) (string, string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		git := filepath.Join(current, ".git")
		_, err := os.Lstat(git)
		if err == nil {
			relative, relativeErr := filepath.Rel(current, filepath.Join(absolute, ".mtix", "hooks.trust"))
			return git, filepath.ToSlash(relative), relativeErr
		}
		if !os.IsNotExist(err) {
			return "", "", err
		}
		if filepath.Dir(current) == current {
			return "", "", nil
		}
	}
}
