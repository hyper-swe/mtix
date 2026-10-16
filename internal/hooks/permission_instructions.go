// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// InstructionAgents reports ids named by tracked instructions, without running
// Git or any repository command. Untracked and non-Git files supply no names.
func InstructionAgents(root string, candidates []string) (result []string, err error) {
	index, prefix, err := instructionIndex(root)
	if err != nil || index == nil {
		return []string{}, err
	}
	paths, err := instructionPaths(root)
	if err != nil {
		return nil, err
	}
	confined, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := confined.Close(); closeErr != nil {
			err = fmt.Errorf("close instruction root: %w", closeErr)
		}
	}()
	named := map[string]bool{}
	for _, name := range paths {
		tracked, err := indexContainsProjectApproval(index, prefix+filepath.ToSlash(name))
		if err != nil {
			return nil, err
		}
		if !tracked {
			continue
		}
		text, err := instructionText(confined, name)
		if err != nil {
			return nil, err
		}
		for _, id := range instructionNames(text, candidates) {
			named[id] = true
		}
	}
	result = make([]string, 0, len(named))
	for id := range named {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func instructionIndex(root string) (data []byte, prefix string, err error) {
	git, relative, err := resolvedGitLocation(root)
	if err != nil || git == "" {
		return nil, "", err
	}
	info, err := os.Lstat(git)
	if err != nil {
		return nil, "", err
	}
	if info.Mode().IsRegular() {
		git, err = instructionWorktreeGit(git)
		if err != nil {
			return nil, "", err
		}
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("instruction Git directory is not verified")
	}
	location, err := os.OpenRoot(git)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if closeErr := location.Close(); closeErr != nil {
			err = fmt.Errorf("close instruction Git root: %w", closeErr)
		}
	}()
	data, err = instructionRead(location, "index", maxApprovalIndex)
	if os.IsNotExist(err) {
		return nil, "", nil
	}
	return data, strings.TrimSuffix(relative, ".mtix/hooks.trust"), err
}

func instructionWorktreeGit(pointer string) (result string, err error) {
	parent, err := os.OpenRoot(filepath.Dir(pointer))
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := parent.Close(); closeErr != nil {
			err = fmt.Errorf("close instruction pointer root: %w", closeErr)
		}
	}()
	data, err := instructionRead(parent, filepath.Base(pointer), 4096)
	if err != nil {
		return "", err
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok || strings.ContainsAny(target, "\r\n\x00") {
		return "", fmt.Errorf("invalid worktree Git pointer")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(pointer), target)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	git, err := os.OpenRoot(target)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := git.Close(); closeErr != nil {
			err = fmt.Errorf("close linked Git root: %w", closeErr)
		}
	}()
	back, err := instructionRead(git, "gitdir", 4096)
	if err != nil {
		return "", err
	}
	backlink := strings.TrimSpace(string(back))
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(target, backlink)
	}
	resolvedBacklink, err := filepath.EvalSymlinks(backlink)
	if err != nil {
		return "", fmt.Errorf("resolve worktree Git backlink: %w", err)
	}
	if resolvedBacklink != filepath.Clean(pointer) {
		return "", fmt.Errorf("worktree Git backlink differs")
	}
	return target, nil
}

func instructionPaths(root string) ([]string, error) {
	paths := []string{}
	for _, name := range []string{"MTIX.md", "CLAUDE.md", "AGENTS.md"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err == nil && info.Mode().IsRegular() {
			paths = append(paths, name)
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	for _, dir := range []string{".claude-plugin/skills", ".codex-plugin/skills", ".agents/skills"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			return appendInstructionPath(root, path, entry, walkErr, &paths)
		})
		if err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func appendInstructionPath(root, path string, entry fs.DirEntry, err error, paths *[]string) error {
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return nil
	}
	if entry.IsDir() {
		return nil
	}
	if filepath.Ext(path) != ".md" {
		return nil
	}
	if len(*paths) >= 256 {
		return fmt.Errorf("instruction file limit exceeded")
	}
	name, err := filepath.Rel(root, path)
	if err == nil {
		*paths = append(*paths, name)
	}
	return err
}

func instructionRead(root *os.Root, name string, limit int64) (data []byte, err error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = fmt.Errorf("close instruction input: %w", closeErr)
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("instruction input is not a regular file")
	}
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("instruction input exceeds limit")
	}
	return data, err
}

func instructionText(root *os.Root, name string) (string, error) {
	data, err := instructionRead(root, name, 1<<20)
	return string(data), err
}

func instructionNames(text string, candidates []string) []string {
	pattern := regexp.MustCompile("mtix agent register[ \\t]+(\"[^\"]+\"|'[^']+'|[^ \\t\\r\\n`]+)")
	result := []string{}
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		name := strings.Trim(match[1], "\"'")
		if !strings.HasPrefix(name, "--") && !strings.ContainsAny(name, "<>{}") {
			result = append(result, name)
		}
	}
	for _, candidate := range candidates {
		boundary := regexp.MustCompile(`(^|[^\pL\pN_.-])` + regexp.QuoteMeta(candidate) + `($|[^\pL\pN_.-])`)
		if boundary.MatchString(text) {
			result = append(result, candidate)
		}
	}
	return result
}
