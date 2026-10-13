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
	logicalRoot, root, err := projectRoots(dir)
	if err != nil {
		return nil, err
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

// scriptInput is one project script's bytes as read for a hook.
type scriptInput struct {
	// prefix is the "--opt=" part when the file is an option's value.
	prefix  string
	path    string
	content []byte
	mode    os.FileMode
}

type legacyPart struct {
	arg     string
	content []byte
}

// hookInputs is everything an approval digest covers, read once.
type hookInputs struct {
	// logical is the project root as entered, root the symlink-resolved one.
	logical, root string
	// script is the command index run as code, or -1.
	script int
	stanza string
	digest string
	legacy []legacyPart
	// scripts maps a command index to the project script it names.
	scripts map[int]scriptInput
}

func projectRoots(dir string) (logical, root string, err error) {
	logical, err = filepath.Abs(filepath.Dir(dir))
	if err != nil {
		return "", "", fmt.Errorf("project path: %w", err)
	}
	root, err = filepath.EvalSymlinks(logical)
	if err != nil {
		return "", "", fmt.Errorf("project location: %w", err)
	}
	return logical, root, nil
}

// collectHookInputs reads a hook's stanza and project scripts once and
// computes the digest over exactly the bytes it returns.
func collectHookInputs(h Hook, logicalRoot, root string) (*hookInputs, error) {
	own, err := marshalHook(h)
	if err != nil {
		return nil, fmt.Errorf("encode hook: %w", err)
	}
	digest := sha256.New()
	digest.Write(own)
	script, err := scriptOperand(h.Exec.Command)
	if err != nil {
		return nil, err
	}
	in := &hookInputs{logical: logicalRoot, root: root, script: script, stanza: string(own), scripts: map[int]scriptInput{}}
	for i, arg := range h.Exec.Command {
		value, prefix := splitOption(arg)
		rel, inside, err := projectScriptLocation(logicalRoot, root, value)
		if err != nil {
			return nil, err
		}
		if inside {
			if err := scriptComponents(root, rel); err != nil {
				return nil, err
			}
		}
		content, mode, ok, readErr := readSnapshotScript(root, value, inside)
		if readErr != nil {
			return nil, readErr
		}
		if !ok {
			continue
		}
		if prefix == "" {
			in.legacy = append(in.legacy, legacyPart{arg: arg, content: content})
		}
		if !inside {
			continue
		}
		path := filepath.Join(root, rel)
		in.scripts[i] = scriptInput{prefix: prefix, path: path, content: content, mode: mode}
		digest.Write([]byte{0})
		digest.Write([]byte(path))
		digest.Write([]byte{0})
		digest.Write(content)
	}
	in.digest = hex.EncodeToString(digest.Sum(nil))
	return in, nil
}

func captureHookSnapshot(snap *hookSnapshot, legacy hash.Hash, h Hook, logicalRoot, root string) error {
	in, err := collectHookInputs(h, logicalRoot, root)
	if err != nil {
		return err
	}
	for _, part := range in.legacy {
		legacy.Write([]byte{0})
		legacy.Write([]byte(part.arg))
		legacy.Write([]byte{0})
		legacy.Write(part.content)
	}
	if slices.Contains(h.Deliver, AdapterExec) {
		snap.digests[h.Name] = in.digest
		snap.stanzas[h.Name] = in.stanza
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

func readSnapshotScript(root, arg string, inside bool) ([]byte, os.FileMode, bool, error) {
	if !inside {
		content, ok := readTrustedScript(root, arg)
		return content, 0, ok, nil
	}
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("project script metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read project script: %w", err)
	}
	return content, info.Mode(), true, nil
}
