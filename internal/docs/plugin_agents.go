// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"fmt"
	"os"
	"path/filepath"
)

// Codex and pi plugin targets (MTIX-27, issue #15).
//
// Codex consumes AGENTS.md natively and configures MCP servers through
// [mcp_servers.*] tables in config.toml (~/.codex/config.toml globally,
// .codex/config.toml project-scoped in trusted projects). pi loads
// AGENTS.md hierarchically and deliberately ships no built-in MCP — its
// users reach MCP servers through the community pi-mcp-adapter
// extension, so the pi install emits guidance instead of config.
//
// Both installs are strictly non-destructive: existing user files are
// never modified. AGENTS.md is write-if-absent (action "skipped" when
// present); an existing config.toml downgrades to action "manual" with
// the exact stanza to add, because merging TOML without a parser risks
// corrupting user configuration.

// codexMCPStanza is the [mcp_servers.mtix] table for Codex's config.toml.
const codexMCPStanza = `[mcp_servers.mtix]
command = "mtix"
args = ["mcp"]
`

// piAdapterGuidance explains how to reach the mtix MCP server from pi.
const piAdapterGuidance = `pi has no built-in MCP support (by design). To use mtix tools from pi,
install the community pi-mcp-adapter extension and register the mtix
server with it:

  server command: mtix mcp

pi loads the installed AGENTS.md automatically; the mtix CLI also works
directly from pi's shell tool. See docs/MCP-SETUP.md ("pi") for details.`

// installCodex installs AGENTS.md and the MCP config for OpenAI Codex.
func (p *PluginInstaller) installCodex(global bool) ([]InstallResult, error) {
	agentsDir := p.projectDir
	codexDir := filepath.Join(p.projectDir, ".codex")
	if global {
		home := userHome()
		// Global Codex instructions live at ~/.codex/AGENTS.md.
		agentsDir = filepath.Join(home, ".codex")
		codexDir = filepath.Join(home, ".codex")
	}

	var results []InstallResult

	agentsResult, err := p.installAgentsFile(agentsDir)
	if err != nil {
		return results, err
	}
	results = append(results, agentsResult)

	confResult, err := installConfigIfAbsent(
		filepath.Join(codexDir, "config.toml"),
		codexMCPStanza,
		"config.toml already exists — add this to it:\n\n"+codexMCPStanza,
	)
	if err != nil {
		return results, err
	}
	results = append(results, confResult)
	return results, nil
}

// installPi installs AGENTS.md for pi and emits pi-mcp-adapter guidance.
func (p *PluginInstaller) installPi(global bool) ([]InstallResult, error) {
	agentsDir := p.projectDir
	if global {
		// pi's global agent context lives at ~/.pi/agent/AGENTS.md.
		agentsDir = filepath.Join(userHome(), ".pi", "agent")
	}

	var results []InstallResult

	agentsResult, err := p.installAgentsFile(agentsDir)
	if err != nil {
		return results, err
	}
	results = append(results, agentsResult)

	results = append(results, InstallResult{
		File:   "mcp",
		Action: "manual",
		Note:   piAdapterGuidance,
	})
	return results, nil
}

// installAgentsFile renders AGENTS.md into dir, write-if-absent: an
// existing file is the user's and is never modified.
func (p *PluginInstaller) installAgentsFile(dir string) (InstallResult, error) {
	return p.installRootDocIfAbsent(dir, "AGENTS.md", "agents.md.tmpl")
}

// installRootDocIfAbsent renders templateName to dir/name, write-if-absent.
// AGENTS.md and CLAUDE.md land outside .mtix/docs/, in territory the user
// owns and hand-edits, so an existing file is never touched: the install
// reports action "skipped" with a note naming the .mtix/docs/ copy that
// mtix does keep current, so the user knows what to merge from and that
// nothing was silently dropped. Both root docs share this one path so the
// clobber policy cannot drift between them.
func (p *PluginInstaller) installRootDocIfAbsent(dir, name, templateName string) (InstallResult, error) {
	if p.templates == nil {
		return InstallResult{}, fmt.Errorf("templates not loaded")
	}

	outPath := filepath.Join(dir, name)
	if fileExists(outPath) {
		return InstallResult{
			File:   name,
			Path:   outPath,
			Action: "skipped",
			Note: fmt.Sprintf(
				"existing %s preserved; regenerate the mtix briefing with 'mtix docs generate' (.mtix/docs/%s) and merge what you need",
				name, name),
		}, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("create %s: %w", dir, err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return InstallResult{}, fmt.Errorf("create %s: %w", outPath, err)
	}
	defer func() { _ = f.Close() }()

	if err := p.templates.ExecuteTemplate(f, templateName, p.data); err != nil {
		return InstallResult{}, fmt.Errorf("render %s: %w", name, err)
	}

	p.logger.Info("plugin install", "file", name, "action", "installed", "path", outPath)
	return InstallResult{File: name, Path: outPath, Action: "installed"}, nil
}

// installConfigIfAbsent writes content to path when no file exists; an
// existing file is never touched and the caller's manual note is
// returned instead.
func installConfigIfAbsent(path, content, manualNote string) (InstallResult, error) {
	name := filepath.Base(path)
	if fileExists(path) {
		return InstallResult{File: name, Path: path, Action: "manual", Note: manualNote}, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return InstallResult{}, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return InstallResult{}, fmt.Errorf("write %s: %w", path, err)
	}
	return InstallResult{File: name, Path: path, Action: "installed"}, nil
}

// userHome resolves the home directory with the same fallback the
// claude-code target uses.
func userHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return home
}
