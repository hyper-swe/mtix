---
name: review
description: Review completed work against mtix task acceptance criteria and safety-critical standards.
---

# Code Review with mtix

## Review Process

1. Run `mtix show <id> --json` to read the task's acceptance criteria
   (`mtix show`, `list`, `ready`, `search`, `tree`, `progress`, `context`, `create` and `decompose --dry-run` print titles and assignees on one line: controls, escapes, bidirectional overrides and isolates and other invisible format characters are removed, line breaks become spaces, and the directional marks U+200E, U+200F and U+061C are kept; `--json` is the exact text)
2. Run `mtix context <id>` to understand the full context chain
3. Verify each acceptance criterion is met
4. Check test coverage meets thresholds (90% overall)
5. Verify no security vulnerabilities introduced

## Verification Checklist

- All acceptance criteria satisfied
- Tests pass with race detector: `go test ./... -race`
- Linter clean: `golangci-lint run`
- No SQL injection vectors (parameterized queries only)
- Error handling: all errors checked and wrapped with context
- No global state introduced
