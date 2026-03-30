#!/usr/bin/env bash
# Build the LLM agent kit tarball for distribution as a release artifact.
# Usage: ./scripts/build-agent-kit.sh <version>
# Output: dist/mtix-agent-kit-v<version>.tar.gz

set -euo pipefail

VERSION="${1:?Usage: build-agent-kit.sh <version>}"
KIT_DIR="dist/mtix-agent-kit-v${VERSION}"

rm -rf "$KIT_DIR"
mkdir -p "$KIT_DIR/skills/references" "$KIT_DIR/mcp-config" "$KIT_DIR/codex"

# Copy skill files
if [ -d ".claude-plugin/skills" ]; then
  cp .claude-plugin/skills/mtix-*.md "$KIT_DIR/skills/" 2>/dev/null || true
  cp .claude-plugin/skills/references/*.md "$KIT_DIR/skills/references/" 2>/dev/null || true
fi

# Copy MCP configs
if [ -d "docs/mcp-config" ]; then
  cp docs/mcp-config/*.json "$KIT_DIR/mcp-config/"
fi

# Copy Codex AGENTS.md
if [ -f "docs/codex/AGENTS.md" ]; then
  cp docs/codex/AGENTS.md "$KIT_DIR/codex/"
fi

# Create README
cat > "$KIT_DIR/README.md" <<'KITREADME'
# mtix Agent Kit

Everything an LLM agent needs to start using mtix.

## Setup

1. Install mtix:
   - Homebrew: `brew install hyper-swe/tap/mtix`
   - Binary: download from https://github.com/hyper-swe/mtix/releases
   - Go: `go install github.com/hyper-swe/mtix/cmd/mtix@latest`

2. Initialize in your project:
   ```bash
   mtix init --prefix PROJ
   ```

## Contents

- `skills/` — Skill files for Claude Code (copy to `.claude/skills/`)
- `skills/references/` — Safety-critical compliance checklists
- `mcp-config/` — MCP server configs for Claude Code, Claude Desktop, Cursor
- `codex/AGENTS.md` — Agent instructions for OpenAI Codex

## Claude Code Plugin (recommended)

Instead of manual setup, install the plugin:
```
/plugin marketplace add hyper-swe/mtix
/plugin install mtix
```

## MCP Configuration

Copy the appropriate config from `mcp-config/` to your IDE settings:
- Claude Code: `.claude/settings.json`
- Claude Desktop: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Cursor: `.cursor/mcp.json`

Replace `/path/to/your/project` with your actual project path.
KITREADME

# Generate MANIFEST.sha256
cd "$KIT_DIR"
find . -type f -not -name 'MANIFEST.sha256' | sort | xargs sha256sum > MANIFEST.sha256
cd - > /dev/null

# Create tarball
mkdir -p dist
tar -czf "dist/mtix-agent-kit-v${VERSION}.tar.gz" -C dist "mtix-agent-kit-v${VERSION}"

echo "Built dist/mtix-agent-kit-v${VERSION}.tar.gz"
echo "Contents:"
tar -tzf "dist/mtix-agent-kit-v${VERSION}.tar.gz" | head -20
