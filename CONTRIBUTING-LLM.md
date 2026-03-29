# Contributing Guidelines for LLM Agents

**Version:** 1.0
**Date:** 2026-03-08
**Applies To:** All LLM agents contributing code to mtix

---

## 1. Mandatory Pre-Work Protocol

Before writing ANY code, the LLM agent MUST:

1. **Read the relevant specification sections** in REQUIREMENTS.md, requirement-ui.md, and/or requirement-prompts.md
2. **Read QUALITY-STANDARDS.md** to understand coverage and testing requirements
3. **Read CODING-STYLE.md** to understand conventions and forbidden patterns
4. **Read this document** (CONTRIBUTING-LLM.md) in full
5. **Check APPROVED-PACKAGES.md** before importing any dependency
6. **Review existing tests** in the area being modified to understand patterns

Failure to follow pre-work protocol results in code that must be rewritten.

---

## 2. Strict TDD Protocol

### 2.1 The Red-Green-Refactor Cycle

Every piece of production code MUST follow this exact sequence:

**Step 1 — RED:** Write a failing test
```go
func TestCreateNode_WithValidInput_ReturnsNodeWithCorrectID(t *testing.T) {
    store := testutil.NewTestStore(t)
    svc := service.NewNodeService(store, nil, nil, slog.Default(), time.Now)

    node, err := svc.CreateNode(ctx, &model.CreateNodeRequest{
        Title:    "Test node",
        Project:  "TEST",
        ParentID: "",
    })

    require.NoError(t, err)
    assert.Equal(t, "TEST-1", node.ID)
    assert.Equal(t, "Test node", node.Title)
    assert.Equal(t, model.StatusOpen, node.Status)
}
```

**Step 2 — GREEN:** Write the minimum production code to pass

**Step 3 — REFACTOR:** Clean up while keeping tests green

### 2.2 Test-First Verification

The agent MUST verify tests fail before writing production code:
```bash
go test ./internal/service/ -run TestCreateNode_WithValidInput -v
# Expected: FAIL (function not yet implemented)
```

Then write the code and verify:
```bash
go test ./internal/service/ -run TestCreateNode_WithValidInput -v
# Expected: PASS
```

### 2.3 Coverage Gate

After implementing a feature, verify coverage:
```bash
go test ./internal/... -coverprofile=coverage.out
go tool cover -func=coverage.out | grep -E "^total:"
# Must be >= 90%
```

---

## 3. Code Contribution Rules

### 3.1 File Creation Rules

- **One concept per file:** A file contains one struct and its methods, or one interface
- **Maximum 500 lines** per source file (excluding tests)
- **Test files MUST be created alongside source files** — no test-free source files
- **Every exported function MUST have a godoc comment**

### 3.2 Import Organization

```go
import (
    // Standard library (alphabetical)
    "context"
    "database/sql"
    "fmt"
    "time"

    // Third-party packages (alphabetical)
    "github.com/spf13/cobra"

    // Internal packages (alphabetical)
    "github.com/hyper-swe/mtix/internal/model"
    "github.com/hyper-swe/mtix/internal/store"
)
```

Three groups separated by blank lines: stdlib, third-party, internal.

### 3.3 Commit Message Format

```
{type}({scope}): {description}

{body}

Refs: MTIX-{task-id}
```

Types: `feat`, `fix`, `test`, `refactor`, `docs`, `perf`, `chore`
Scopes: `store`, `service`, `cli`, `api`, `mcp`, `grpc`, `model`, `docs`

Example:
```
feat(store): implement node creation with atomic sequence generation

Adds CreateNode to SQLiteStore with:
- Atomic sequence counter via INSERT ... ON CONFLICT DO UPDATE
- Parent validation (FR-3.9: reject if parent is terminal)
- Content hash computation (FR-3.7)
- Progress recalculation in same transaction (FR-5.7)

Refs: MTIX-1.1.1.1
```

### 3.4 Git Branching and Safety-Net Protocol

**MANDATORY READING:** Before creating your first branch, read the **GIT BRANCHING AND SAFETY-NET PROTOCOL** section in `CLAUDE.md`. It defines the complete branch lifecycle, naming conventions, commit granularity requirements, multi-agent conflict prevention rules, and recovery procedures.

Key points summarized here for quick reference:
- **One branch per task** — named `{type}/MTIX-{id}/{short-desc}`
- **Never work on main** — main is always deployable
- **Push after every commit** — this is your backup until mgit exists
- **Minimum 3 commits per task** — RED (failing test), GREEN (passing), REFACTOR
- **Fast-forward merge only** — rebase onto main before merging
- **Delete branch after merge** — keep the branch tree clean

The full protocol in CLAUDE.md is authoritative. This summary does not replace it.

### 3.5 Function Size Limits

- Maximum function body: 50 lines (excluding comments and blank lines)
- Maximum cyclomatic complexity: 15
- Maximum parameters: 5 (use struct for more)
- If a function exceeds these limits, refactor into smaller functions

---

## 4. SQL Rules (Critical)

### 4.1 Absolute Rule: Parameterized Queries Only

```go
// ✅ CORRECT
db.QueryContext(ctx, "SELECT * FROM nodes WHERE id = ?", id)

// ✅ CORRECT — LIKE with parameter
db.QueryContext(ctx, `SELECT * FROM nodes WHERE id LIKE ? ESCAPE '\'`, prefix+".%")

// ❌ FORBIDDEN — String concatenation
db.QueryContext(ctx, "SELECT * FROM nodes WHERE id = '"+id+"'")

// ❌ FORBIDDEN — fmt.Sprintf for SQL
query := fmt.Sprintf("SELECT * FROM nodes WHERE id = '%s'", id)
```

Violation of this rule is an immediate rejection. No exceptions.

### 4.2 PRAGMA Requirements

Every database connection MUST execute:
```sql
PRAGMA foreign_keys = ON;
```

The write connection MUST also execute:
```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
```

### 4.3 Transaction Requirements

- All write operations MUST be wrapped in a transaction
- Progress rollup MUST happen in the same transaction as the triggering change
- Use the `withTx` helper pattern defined in CODING-STYLE.md

---

## 5. Error Handling Rules

### 5.1 Never Swallow Errors

```go
// ❌ FORBIDDEN
_ = file.Close()

// ✅ CORRECT
if err := file.Close(); err != nil {
    s.logger.Error("failed to close file", slog.Any("error", err))
}
```

### 5.2 Always Wrap Errors with Context

```go
// ✅ CORRECT
return fmt.Errorf("create node %s: %w", id, err)

// ❌ WRONG — no context
return err
```

### 5.3 Use Sentinel Errors

Map to the predefined sentinel errors in `model/errors.go`. Do NOT create ad-hoc error strings.

---

## 6. Testing Rules

### 6.1 Required Test Categories Per Feature

For every feature implemented, the agent MUST write:

1. **Happy path tests** — Normal successful operation
2. **Error path tests** — Every possible error condition
3. **Boundary tests** — Edge cases (empty input, max length, zero values)
4. **State machine tests** — Valid and invalid transitions
5. **Concurrency tests** — When the feature involves shared state
6. **Integration tests** — Cross-component interactions

### 6.2 Test Helpers

Use the shared test helpers in `internal/testutil/`:

```go
// Create a fresh test database
store := testutil.NewTestStore(t)

// Create a test node with defaults
node := testutil.MakeNode(t, store, testutil.WithTitle("Test"), testutil.WithParent("PROJ-1"))

// Assert node state
testutil.AssertNodeStatus(t, store, "PROJ-1.1", model.StatusOpen)
testutil.AssertProgress(t, store, "PROJ-1", 0.5)
```

### 6.3 Test Data Rules

- Test data MUST use the project prefix `TEST`
- Test node titles MUST be descriptive of the test scenario
- Tests MUST NOT use production data or real identifiers
- Each test MUST create its own data — no shared test state

### 6.4 Assertion Library

Use `testify` for assertions:
- `require` for preconditions (test stops on failure)
- `assert` for assertions (test continues to report all failures)

```go
require.NoError(t, err)           // Precondition: no error
assert.Equal(t, expected, actual) // Assertion: values match
assert.Nil(t, node.ClosedAt)     // Assertion: nil check
```

### 6.5 Test File Naming

Test files MUST be named after the functionality they test, not after why they were written:

```
# ✅ CORRECT — describes what is tested
store_operations_test.go
cli_edge_cases_test.go
service_edge_cases_test.go
mcp_tool_integration_test.go

# ❌ FORBIDDEN — describes why the file exists
coverage_boost_test.go
coverage_boost2_test.go
gap_filler_test.go
```

---

## 7. Anti-Stub Policy (CRITICAL)

### 7.1 Stub Implementations Are NOT Done

A task is **NOT done** if the implementation contains any of the following patterns:

```go
// ❌ FORBIDDEN — printing "not implemented" and returning success
fmt.Println("X not yet implemented")
return nil

// ❌ FORBIDDEN — fake success response from API handler
c.JSON(200, gin.H{"status": "completed", "message": "X completed"})
// ...but the handler does NO actual work

// ❌ FORBIDDEN — placeholder comment as implementation
// In a full implementation, this would:
// 1. Do X
// 2. Do Y
<-ctx.Done()
return nil

// ❌ FORBIDDEN — stub with integration pending comment
return SuccessResult("X requested (integration pending MTIX-N)")
```

### 7.2 What To Do Instead

If the underlying implementation exists but CLI/API wiring is not done:
- **Wire it.** Call the real service/store method. That is the task.
- If wiring is blocked by missing infrastructure, mark the task as `"blocked"` with a clear reason.
- **NEVER** mark a task as `"done"` with a stub that compiles but does nothing.

If the feature is genuinely not implemented yet:
- Mark the task as `"open"` or `"blocked"`.
- Document what is missing in the task description.
- **NEVER** ship a function that returns fake success.

### 7.3 Why This Matters

In safety-critical systems, a stub that returns success is worse than a function that returns an error. A fake `{"status": "completed"}` response from a GC handler deceives operators into believing garbage collection ran when it did not. A backup command that prints "done" without creating a backup gives false confidence that data is protected. These are not incomplete features — they are **active deceptions** that undermine system integrity.

### 7.4 Verification

Before marking any task as done, grep for stub patterns:

```bash
grep -rn '"not yet implemented"\|"not implemented"\|"integration pending"' \
  --include='*.go' --exclude='*_test.go'
```

If any matches are found in your changed files, the task is NOT done.

---

## 8. Dependency Rules

### 7.1 Package Usage

- ONLY use packages listed in APPROVED-PACKAGES.md
- If a new package is needed, follow PACKAGE-APPROVAL-PROCESS.md
- NEVER introduce a dependency without checking the approved list first
- Prefer stdlib over third-party when feasible

### 7.2 Import Verification

Before adding any import, verify:
```bash
grep -q "package-name" APPROVED-PACKAGES.md || echo "UNAPPROVED DEPENDENCY"
```

---

## 9. Architecture Layer Rules

### 8.1 Dependency Direction

```
cmd/ → service/ → store/ → model/
       ↑
api/ ──┘
mcp/ ──┘
```

- `model/` depends on nothing (pure domain types)
- `store/` depends only on `model/`
- `service/` depends on `store/` and `model/`
- `cmd/`, `api/`, `mcp/` depend on `service/` and `model/`
- NEVER import `cmd/` from `internal/`
- NEVER import `api/` from `store/`

### 8.2 Interface Location

- **Default rule:** Narrow, single-consumer interfaces SHOULD be defined in the package that USES them (consumer side). For example, a `NodeCreator` interface used only in a specific test or a single handler belongs in that consumer's package.
- **Shared contract exception:** Interfaces consumed by multiple packages (service, CLI, API, MCP, gRPC) live in their own package as the canonical contract. The `store.Store` interface lives in `internal/store/store.go` because it is the central data access contract shared across all layers — not a narrow consumer-side interface.
- The `service` package defines any interfaces it needs from external systems (e.g., `EventBroadcaster`)
- **Rule of thumb:** If only one package imports an interface, define it in that package. If multiple packages import it, give it its own package or place it at the provider.

---

## 10. Documentation Rules

### 9.1 Godoc Comments

Every exported symbol MUST have a godoc comment:

```go
// CreateNode creates a new node in the hierarchy.
// It generates a dot-notation ID using atomic sequence counters (FR-2.7),
// validates the parent status (FR-3.9), computes content_hash (FR-3.7),
// and recalculates parent progress (FR-5.7) in the same transaction.
//
// Returns ErrInvalidInput if the parent is in a terminal status.
// Returns ErrAlreadyExists if a node with the generated ID already exists.
func (s *NodeService) CreateNode(ctx context.Context, req *CreateNodeRequest) (*model.Node, error) {
```

### 9.2 Spec Traceability

Every function that implements a requirement MUST reference the FR/NFR number:

```go
// TransitionStatus validates and executes a status transition per FR-3.5.
// Idempotent transitions return success per FR-7.7a.
func (s *NodeService) TransitionStatus(ctx context.Context, id string, target model.Status, opts TransitionOpts) error {
```

---

## 11. Pre-Commit Checklist

Before committing any code, the agent MUST verify:

- [ ] All tests pass: `go test ./...`
- [ ] Coverage meets threshold: `go test ./... -coverprofile=c.out && go tool cover -func=c.out`
- [ ] Linter clean: `golangci-lint run`
- [ ] No unapproved imports
- [ ] All exports have godoc comments
- [ ] Commit message follows format
- [ ] Tests were written BEFORE production code
- [ ] No forbidden patterns (global state, string SQL concat, init(), panic for errors)
- [ ] FR/NFR references in comments where applicable

---

## 12. What NOT To Do

1. **Do NOT write production code without tests first**
2. **Do NOT use string concatenation for SQL queries**
3. **Do NOT import unapproved packages**
4. **Do NOT skip error handling**
5. **Do NOT use global state or singletons**
6. **Do NOT use init() functions**
7. **Do NOT bypass the service layer from handlers**
8. **Do NOT use time.Now() directly — inject a clock**
9. **Do NOT create files longer than 500 lines**
10. **Do NOT merge without passing all quality gates**

---

*Every line of code is a liability. Make each one earn its place with a test.*
