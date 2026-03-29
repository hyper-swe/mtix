# Contributing to mtix

Thank you for your interest in contributing to mtix! This document covers the engineering standards, coding conventions, and workflow that all contributions must follow.

mtix is built to **safety-critical standards** — it is designed for environments where software reliability directly impacts outcomes. Every contribution must reflect this commitment to correctness.

**Core Principle: "If it's not tested, it doesn't work. If it's not documented, it doesn't exist."**

**Applicable Standards:** DO-178C (Airborne Software), IEC 62304 (Medical Devices), NASA-STD-8739.8 (Software Assurance), MIL-STD-498 (Defense Software), OWASP ASVS Level 2.

---

## The Iron Rule: Use mtix to Build mtix

**Every change — feature, bug fix, refactor, documentation update, even a one-line fix — MUST have an mtix task created BEFORE any code is written.** We use mtix to manage mtix development. No exceptions.

This isn't bureaucracy — it's how we prove the tool works. If mtix can't manage its own development, it can't manage yours. Every contribution that ships without a tracked task is a gap in our audit trail and a missed opportunity to validate our own product.

### Creating a Task

Before you write a single line of code:

```bash
# Create a task with full context
mtix create "Fix null parent validation in CreateNode" \
  --description "CreateNode accepts null parent_id without validation, causing a foreign key constraint error at the SQLite layer instead of returning ErrInvalidInput at the service layer. This violates FR-3.9 which requires parent status validation before child creation." \
  --prompt "File: internal/service/node_service.go, function CreateNode(). Add a check after parent lookup: if parent is nil and parent_id was provided, return fmt.Errorf(\"parent %s: %w\", req.ParentID, model.ErrNotFound). Add test in internal/service/node_service_test.go: TestCreateNode_NonExistentParent_ReturnsNotFound." \
  --acceptance "1. CreateNode with non-existent parent_id returns ErrNotFound. 2. CreateNode with empty parent_id still creates a root node successfully. 3. Test covers both cases. 4. No regression in existing parent validation tests."

# For larger work, decompose into subtasks
mtix decompose MTIX-42 --file - <<'EOF'
{"title": "Add parent existence check", "description": "...", "prompt": "...", "acceptance": "..."}
{"title": "Add test coverage for null parent", "description": "...", "prompt": "...", "acceptance": "..."}
EOF

# Populate every leaf with actionable context
mtix update MTIX-42.1 \
  --prompt "In internal/service/node_service.go, function CreateNode(), after line 47 where parent is fetched via s.store.GetNode()..." \
  --acceptance "1. Returns ErrNotFound when parent_id is 'NONEXISTENT'. 2. Error message includes the parent_id for debugging."
```

### The Completeness Test

Every task you create must pass this test:

> *"Can a completely different contributor — with zero context about why this task exists — pick it up and execute it correctly by reading only the task and its parent chain?"*

If the answer is no, your task is incomplete. Add the missing details:

- **description** — Why this task exists, what problem it solves, what the scope is
- **prompt** — Exact files to modify, function names, inputs/outputs, error conditions, edge cases, test scenarios
- **acceptance** — Specific, testable criteria that define "done" — not vague goals like "works correctly," but verifiable outcomes like "returns ErrNotFound when parent_id does not exist in the database"

### The Context Chain

mtix uses dot-notation IDs (`MTIX-42.1.3`) to form a context chain. Each level adds detail:

```
MTIX-42        "Harden input validation"              ← why (business goal)
MTIX-42.1      "Validate parent references in CRUD"    ← what (technical scope)
MTIX-42.1.3    "Add null parent check in CreateNode"   ← how (exact instruction)
```

When you run `mtix context MTIX-42.1.3`, you get the assembled briefing from root to leaf — the complete picture. Write your tasks so this chain tells a coherent story from business goal to implementation detail.

### Workflow

```bash
# 1. Create or find a task
mtix create "..." --description "..." --prompt "..." --acceptance "..."
# or
mtix ready                    # Find tasks available for pickup

# 2. Read the full context
mtix context MTIX-{id}        # Assembled briefing from root → leaf

# 3. Claim it
mtix claim MTIX-{id} --agent your-name

# 4. Do the work (TDD: red → green → refactor)

# 5. Verify all acceptance criteria are met

# 6. Export and commit
mtix export                   # Export task state to tasks.json
git add .mtix/tasks.json      # Include task state in your commit
git commit -m "feat(store): add null parent validation

Refs: MTIX-42.1.3"

# 7. Mark done
mtix done MTIX-{id}
mtix export                   # Re-export after status change
```

### Export Before Push

Always run `mtix export` before pushing. This writes the current task state to `.mtix/tasks.json`, which is tracked in git. This ensures:
- Other contributors can see task status without running mtix locally
- The task state is versioned alongside the code it describes
- Progress is visible in pull request diffs

### What Happens If You Skip This

- Your PR will be rejected. Every PR must reference an MTIX task ID.
- Untracked work breaks traceability — we can't audit what changed or why.
- It signals that the contributor doesn't trust mtix enough to use it, which undermines the project's credibility.

---

## Before You Start

1. **Read the relevant requirements.** Every feature references FR (Functional Requirement) and NFR (Non-Functional Requirement) numbers in `REQUIREMENTS.md`. Read those sections before implementing.

2. **Read the governing documents:**
   - `QUALITY-STANDARDS.md` — coverage targets, static analysis, security requirements
   - `CODING-STYLE.md` — architecture patterns, naming conventions, forbidden patterns
   - `TDD-WORKFLOW.md` — the exact test-driven process you must follow
   - `APPROVED-PACKAGES.md` — the only dependencies you may use

3. **Review existing code** in the area you're modifying. Understand the patterns already established. Do not introduce inconsistencies.

---

## Test-Driven Development — Non-Negotiable

All contributions MUST follow strict TDD:

```
1. READ the requirement (FR/NFR from REQUIREMENTS.md)
2. WRITE failing test(s) — the test defines the behavior
3. RUN test → CONFIRM IT FAILS (red)
4. WRITE the minimum production code to make the test pass
5. RUN test → CONFIRM IT PASSES (green)
6. REFACTOR — clean up while keeping all tests green
7. CHECK coverage ≥ 90%
8. COMMIT — test and production code together
```

### Test Naming Convention

```go
func Test{Function}_{Condition}_{ExpectedResult}(t *testing.T)
```

### Required Test Categories

For every feature, you must write:

1. **Happy path tests** — Normal successful operations
2. **Error path tests** — Every documented error condition
3. **Boundary tests** — Edge cases: empty input, max length, zero values
4. **State machine tests** — Valid and invalid transitions
5. **Idempotency tests** — Duplicate requests produce correct results
6. **Concurrency tests** — When shared state is involved (use `-race` flag)
7. **Data integrity tests** — Checksums verify, corrupt data detected

### Coverage Requirements

| Layer | Line Coverage | Branch Coverage |
|-------|--------------|-----------------|
| Storage (SQLite) | 95% | 90% |
| Service (Business Logic) | 95% | 90% |
| Model / CLI / API / MCP | 90% | 85% |
| Overall Project | **90% minimum** | 85% |

---

## Architecture

### Layered Architecture (Strict)

```
CLI Commands / REST Handlers / gRPC Handlers / MCP Tools
                          │
                    Service Layer  ← ALL business logic lives here
                          │
                    Store Interface ← Data access contract
                          │
                    SQLite Implementation
```

**Rules:**
- Handlers MUST NOT access the store directly — always go through the service layer
- The store layer contains ONLY data access — no business rules, no validation
- The service layer orchestrates business logic, calls the store, broadcasts events
- `model/` depends on NOTHING — it is pure domain types
- Dependency direction: `cmd/` → `service/` → `store/` → `model/`

### Dependency Injection — No Global State

```go
type NodeService struct {
    store       Store
    broadcaster EventBroadcaster
    config      *Config
    logger      *slog.Logger
    clock       func() time.Time  // Injected clock for testability
}
```

**Forbidden:** Global variables (except sentinel errors and constants), `init()` functions, singletons, package-level state.

### Clock Injection

```go
// FORBIDDEN — direct time.Now()
node.CreatedAt = time.Now()

// CORRECT — injected clock
node.CreatedAt = s.clock()
```

---

## SQL Rules — Absolute and Unbreakable

### Parameterized Queries Only

```go
// CORRECT
db.QueryContext(ctx, "SELECT * FROM nodes WHERE id = ?", id)

// FORBIDDEN — SQL injection vector
db.QueryContext(ctx, "SELECT * FROM nodes WHERE id = '"+id+"'")
```

**This rule has ZERO exceptions.** All SQL uses parameterized queries, always.

### Additional SQL Rules

- `PRAGMA foreign_keys = ON` on every connection
- `PRAGMA journal_mode = WAL` on write connection
- All writes in transactions via `withTx` helper
- Separate connection pools: `writeDB.SetMaxOpenConns(1)` for serialized writes

---

## Error Handling

```go
// CORRECT — wrap with context, use %w for error chain
return fmt.Errorf("create node %s: %w", id, err)

// FORBIDDEN — swallowing errors
_ = file.Close()

// FORBIDDEN — losing error chain
return fmt.Errorf("failed: %s", err)  // use %w, not %s
```

Every error MUST be checked, wrapped with context, and use sentinel errors from `model/errors.go`.

---

## Naming and Formatting

| Element | Convention | Example |
|---------|-----------|---------|
| Package | lowercase, single word | `store`, `model`, `service` |
| Exported type | PascalCase | `NodeService`, `SQLiteStore` |
| Interface | Behavior-based, no "I" prefix | `Store`, `EventBroadcaster` |
| Errors | `Err` prefix | `ErrNotFound`, `ErrInvalidTransition` |
| Tests | `Test{Function}_{Scenario}_{Expected}` | `TestCreateNode_DuplicateID_ReturnsError` |
| JSON fields | `snake_case` | `parent_id`, `created_at` |

### Import Organization

Three groups, separated by blank lines: standard library, third-party, internal.

---

## Function and File Limits

- **Maximum function body:** 50 lines
- **Maximum cyclomatic complexity:** 15
- **Maximum cognitive complexity:** 20
- **Maximum parameters:** 5 (use a struct for more)
- **Maximum file length:** 500 lines (excluding tests)

---

## Approved Dependencies

You may ONLY use the dependencies listed in `APPROVED-PACKAGES.md`. Using an unapproved package requires documented justification and approval.

---

## Commit Message Format

```
{type}({scope}): {description}

{body — what changed and why}

Refs: MTIX-{task-id}
```

**Types:** `feat`, `fix`, `test`, `refactor`, `docs`, `perf`, `chore`
**Scopes:** `store`, `service`, `cli`, `api`, `mcp`, `grpc`, `model`, `docs`

---

## Security Requirements

1. **All user input is hostile.** Validate at boundaries. Input length limits enforced.
2. **SQL injection prevention.** Parameterized queries only — no exceptions.
3. **XSS prevention.** All user content rendered through sanitized markdown.
4. **CSRF protection.** All mutations require `X-Requested-With: mtix` header.
5. **Localhost binding by default.** Server binds to `127.0.0.1`.
6. **Dependency supply chain.** Only approved packages. `go.sum` committed and verified.

---

## Verification Checklist — Before Every PR

```bash
# 1. All tests pass
go test ./... -count=1

# 2. Race detector clean
go test ./... -race -count=1

# 3. Coverage meets threshold (≥ 90%)
go test ./... -coverprofile=cover.out -count=1
go tool cover -func=cover.out | tail -1

# 4. Linter clean
golangci-lint run

# 5. Build succeeds
go build -o mtix ./cmd/mtix/
```

All checks must pass. Do not submit PRs with failing tests or linter warnings.

---

## License

By contributing to mtix, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
