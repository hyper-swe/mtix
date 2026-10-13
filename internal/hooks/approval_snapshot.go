// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type hookSnapshot struct {
	errors  map[string]error
	stanzas map[string]string
	legacy  string
	digests map[string]string
}

func approvalSnapshot(dir string) (*hookSnapshot, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "hooks.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read hooks: %w", err)
	}
	var cfg Config
	if parseErr := yaml.Unmarshal(raw, &cfg); parseErr != nil {
		return nil, fmt.Errorf("parse hooks: %w", parseErr)
	}
	cfg, _ = validate(cfg)
	legacy := sha256.New()
	legacy.Write(raw)
	snap := &hookSnapshot{errors: map[string]error{}, digests: map[string]string{}, stanzas: map[string]string{}}
	logicalRoot, err := filepath.Abs(filepath.Dir(dir))
	if err != nil {
		return nil, fmt.Errorf("project path: %w", err)
	}
	root, err := filepath.EvalSymlinks(logicalRoot)
	if err != nil {
		return nil, fmt.Errorf("project location: %w", err)
	}
	for _, h := range cfg.Hooks {
		if h.Exec == nil {
			continue
		}
		if err := captureHookSnapshot(snap, legacy, h, logicalRoot, root); err != nil {
			snap.errors[h.Name] = err
		}
	}

	snap.legacy = hex.EncodeToString(legacy.Sum(nil))
	return snap, nil
}

func splitPath(path string) []string { return strings.Split(path, string(filepath.Separator)) }

func scriptComponents(root, rel string) error {
	current := root
	for _, part := range splitPath(rel) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("script metadata: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("script path requires symlink-free components")
		}
	}
	return nil
}

func captureHookSnapshot(snap *hookSnapshot, legacy hash.Hash, h Hook, logicalRoot, root string) error {
	own, err := marshalHook(h)
	if err != nil {
		return fmt.Errorf("encode hook: %w", err)
	}
	digest := sha256.New()
	digest.Write(own)
	for _, arg := range h.Exec.Command {
		rel, inside, err := projectScriptLocation(logicalRoot, root, arg)
		if err != nil {
			return err
		}
		if inside {
			if err := scriptComponents(root, rel); err != nil {
				return err
			}
		}
		content, ok, readErr := readSnapshotScript(root, arg, inside)
		if readErr != nil {
			return readErr
		}
		if !ok {
			continue
		}
		legacy.Write([]byte{0})
		legacy.Write([]byte(arg))
		legacy.Write([]byte{0})
		legacy.Write(content)
		if !inside {
			continue
		}
		digest.Write([]byte{0})
		digest.Write([]byte(filepath.Join(root, rel)))
		digest.Write([]byte{0})
		digest.Write(content)
	}
	if slices.Contains(h.Deliver, AdapterExec) {
		snap.digests[h.Name] = hex.EncodeToString(digest.Sum(nil))
		snap.stanzas[h.Name] = string(own)
	}
	return nil
}

func projectScriptLocation(logicalRoot, root, arg string) (string, bool, error) {
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, arg)
	}
	rel, err := filepath.Rel(logicalRoot, path)
	if err == nil && (rel == ".." || slices.Contains(splitPath(rel), "..")) {
		rel, err = filepath.Rel(root, path)
	}
	if err != nil {
		return "", false, fmt.Errorf("script location: %w", err)
	}
	return rel, rel != ".." && !filepath.IsAbs(rel) && !slices.Contains(splitPath(rel), ".."), nil
}

func (s *hookSnapshot) combinedError() error {
	var result error
	for _, err := range s.errors {
		result = errors.Join(result, err)
	}
	return result
}

func readSnapshotScript(root, arg string, inside bool) ([]byte, bool, error) {
	if !inside {
		content, ok := readTrustedScript(root, arg)
		return content, ok, nil
	}
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, arg)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("project script metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read project script: %w", err)
	}
	return content, true, nil
}
