# TDD Workflow

**Version:** 1.0
**Date:** 2026-03-08

---

## 1. Test-Driven Development Cycle

Every feature in mtix follows a strict Red-Green-Refactor cycle. No production code exists without a test that demanded its creation.

### 1.1 The Cycle

```
┌────────────────────────────────────────────────┐
│                                                │
│   1. READ the requirement (FR/NFR)             │
│              │                                 │
│              ▼                                 │
│   2. WRITE failing test(s)                     │
│              │                                 │
│              ▼                                 │
│   3. RUN test → confirm RED (FAIL)             │
│              │                                 │
│              ▼                                 │
│   4. WRITE minimum production code             │
│              │                                 │
│              ▼                                 │
│   5. RUN test → confirm GREEN (PASS)           │
│              │                                 │
│              ▼                                 │
│   6. REFACTOR (keep tests green)               │
│              │                                 │
│              ▼                                 │
│   7. CHECK coverage ≥ 90%                      │
│              │                                 │
│              ▼                                 │
│   8. COMMIT with test + code together          │
│                                                │
└────────────────────────────────────────────────┘
```

### 1.2 Workflow Commands

```bash
# Step 3: Confirm RED
go test ./internal/store/sqlite/ -run TestCreateNode -v -count=1
# Expected: FAIL

# Step 5: Confirm GREEN
go test ./internal/store/sqlite/ -run TestCreateNode -v -count=1
# Expected: PASS

# Step 7: Check coverage
go test ./internal/store/sqlite/ -coverprofile=cover.out -count=1
go tool cover -func=cover.out | tail -1
# Expected: total: (statements) 90.0%+

# Full suite
go test ./... -race -count=1

# Coverage report
go test ./... -coverprofile=cover.out -count=1
go tool cover -html=cover.out -o coverage.html
```

---

## 2. Test Pyramid

```
         ╱ E2E Tests ╲              (~10% of tests)
        ╱  CLI + API   ╲            Full stack, slow
       ╱────────────────╲
      ╱ Integration Tests ╲         (~20% of tests)
     ╱  Service + Store     ╲       Cross-component
    ╱────────────────────────╲
   ╱      Unit Tests          ╲     (~70% of tests)
  ╱  Functions in isolation    ╲    Fast, deterministic
 ╱──────────────────────────────╲
```

### 2.1 Unit Tests (70%)

- Test individual functions/methods in isolation
- Mock all dependencies
- MUST run in < 1 second per test
- MUST be deterministic — no randomness, no time dependency

### 2.2 Integration Tests (20%)

- Test component interactions (store + service, service + broadcaster)
- Use real SQLite (temp file via `t.TempDir()`) — no mocking the database. Temp files are used instead of `:memory:` to support the dual read/write connection pool pattern (see §4.1).
- MUST run in < 5 seconds per test
- Located in `internal/integration/`

### 2.3 End-to-End Tests (10%)

- Test full CLI commands and API endpoints
- Spin up real server, execute real commands
- MUST run in < 30 seconds per test
- Located in `e2e/`

---

## 3. Test Categories Required Per Feature

For each feature implemented, the following test categories are MANDATORY:

### 3.1 Happy Path Tests

Test the normal, expected behavior:
```go
func TestCreateNode_ValidInput_CreatesNode(t *testing.T) {
    // Setup
    store := testutil.NewTestStore(t)
    svc := service.NewNodeService(store, ...)

    // Execute
    node, err := svc.CreateNode(ctx, &CreateNodeRequest{
        Title:   "Valid node",
        Project: "TEST",
    })

    // Assert
    require.NoError(t, err)
    assert.Equal(t, "TEST-1", node.ID)
    assert.Equal(t, model.StatusOpen, node.Status)
}
```

### 3.2 Error Path Tests

Test every documented error condition:
```go
func TestCreateNode_UnderCancelledParent_ReturnsInvalidInput(t *testing.T) {
    store := testutil.NewTestStore(t)
    svc := service.NewNodeService(store, ...)

    // Create and cancel parent
    parent, _ := svc.CreateNode(ctx, &CreateNodeRequest{Title: "Parent", Project: "TEST"})
    _ = svc.CancelNode(ctx, parent.ID, "test reason")

    // Attempt to create child under cancelled parent
    _, err := svc.CreateNode(ctx, &CreateNodeRequest{
        Title:    "Child",
        Project:  "TEST",
        ParentID: parent.ID,
    })

    // Assert FR-3.9: cannot create under terminal parent
    require.Error(t, err)
    assert.ErrorIs(t, err, ErrInvalidInput)
}
```

### 3.3 Boundary Tests

Test edge cases and limits:
```go
func TestCreateNode_MaxTitleLength_Succeeds(t *testing.T) { /* 500 chars */ }
func TestCreateNode_TitleTooLong_ReturnsInvalidInput(t *testing.T) { /* 501 chars */ }
func TestCreateNode_EmptyTitle_ReturnsInvalidInput(t *testing.T) {}
func TestCreateNode_Depth50_EmitsWarning(t *testing.T) { /* FR-1.1a */ }
func TestCreateNode_Depth51_StillSucceeds(t *testing.T) { /* advisory limit */ }
```

### 3.4 State Machine Tests

Exhaustive testing of every valid and invalid transition (FR-3.5):
```go
func TestTransition_AllValidTransitions(t *testing.T) {
    validTransitions := []struct {
        from   model.Status
        action string
        to     model.Status
    }{
        {model.StatusOpen, "claim", model.StatusInProgress},
        {model.StatusOpen, "defer", model.StatusDeferred},
        {model.StatusOpen, "cancel", model.StatusCancelled},
        {model.StatusInProgress, "done", model.StatusDone},
        {model.StatusInProgress, "defer", model.StatusDeferred},
        // ... all valid transitions
    }

    for _, tt := range validTransitions {
        t.Run(fmt.Sprintf("%s->%s via %s", tt.from, tt.to, tt.action), func(t *testing.T) {
            // ...
        })
    }
}

func TestTransition_AllInvalidTransitions(t *testing.T) {
    invalidTransitions := []struct {
        from   model.Status
        action string
    }{
        {model.StatusDone, "claim"},
        {model.StatusBlocked, "done"},
        {model.StatusInvalidated, "done"},
        // ... all invalid transitions
    }

    for _, tt := range invalidTransitions {
        t.Run(fmt.Sprintf("%s via %s", tt.from, tt.action), func(t *testing.T) {
            // ... assert ErrInvalidTransition
        })
    }
}
```

### 3.5 Idempotency Tests (FR-7.7a)

```go
func TestDone_AlreadyDone_ReturnsIdempotent(t *testing.T) { /* 200 OK with idempotent: true */ }
func TestClaim_AlreadyClaimedBySameAgent_ReturnsIdempotent(t *testing.T) {}
func TestDefer_SameUntil_ReturnsIdempotent(t *testing.T) {}
func TestDefer_DifferentUntil_UpdatesDeferUntil(t *testing.T) { /* NOT idempotent */ }
```

### 3.6 Concurrency Tests

```go
func TestConcurrentSiblingCompletion_ProgressCorrect(t *testing.T) {
    // FR-5.7 test scenario: 3 agents complete siblings simultaneously
    store := testutil.NewTestStore(t)
    // ... create parent with 3 children

    var wg sync.WaitGroup
    for _, childID := range []string{"TEST-1.1", "TEST-1.2", "TEST-1.3"} {
        wg.Add(1)
        go func(id string) {
            defer wg.Done()
            err := svc.MarkDone(ctx, id, "completed")
            assert.NoError(t, err)
        }(childID)
    }
    wg.Wait()

    // Parent progress MUST be 100%
    parent, _ := svc.GetNode(ctx, "TEST-1")
    assert.Equal(t, 1.0, parent.Progress)
}
```

### 3.7 Data Integrity Tests

```go
func TestBackup_CorruptedFile_DetectedAndDeleted(t *testing.T) { /* FR-6.3a */ }
func TestExport_ChecksumVerified_OnImport(t *testing.T) { /* FR-7.8 */ }
func TestStartup_CorruptDB_RefusesToStart(t *testing.T) { /* NFR-2.6a */ }
func TestVerify_AllChecksPass_ExitCode0(t *testing.T) {}
func TestVerify_FKViolation_ReportsFailure(t *testing.T) {}
```

---

## 4. Test Utilities

### 4.1 Test Store Factory

```go
// internal/testutil/store.go

// NewTestStore creates a fresh SQLite store for testing using a temp file.
// Uses a temp file (not :memory:) so the dual read/write connection pool
// pattern from CODING-STYLE.md §5.3 works correctly — in-memory databases
// create separate databases per sql.Open() call, breaking the dual-pool model.
// The store and temp file are automatically cleaned up when the test completes.
func NewTestStore(t *testing.T) store.Store {
    t.Helper()
    dir := t.TempDir() // Auto-cleaned by testing framework
    s, err := sqlite.NewSQLiteStore(filepath.Join(dir, "test.db"))
    require.NoError(t, err)
    t.Cleanup(func() { require.NoError(t, s.Close()) })
    return s
}
```

### 4.2 Node Builder

```go
// internal/testutil/builders.go

type NodeOption func(*model.CreateNodeRequest)

func WithTitle(title string) NodeOption {
    return func(r *model.CreateNodeRequest) { r.Title = title }
}

func WithParent(parentID string) NodeOption {
    return func(r *model.CreateNodeRequest) { r.ParentID = parentID }
}

func WithStatus(status model.Status) NodeOption {
    return func(r *model.CreateNodeRequest) { r.Status = status }
}

// MakeNode creates a test node with the given options.
func MakeNode(t *testing.T, svc *service.NodeService, opts ...NodeOption) *model.Node {
    t.Helper()
    req := &model.CreateNodeRequest{
        Title:   "Test Node",
        Project: "TEST",
    }
    for _, opt := range opts {
        opt(req)
    }
    node, err := svc.CreateNode(context.Background(), req)
    require.NoError(t, err)
    return node
}
```

### 4.3 Assertion Helpers

```go
// internal/testutil/assertions.go

func AssertNodeStatus(t *testing.T, svc *service.NodeService, id string, expected model.Status) {
    t.Helper()
    node, err := svc.GetNode(context.Background(), id)
    require.NoError(t, err)
    assert.Equal(t, expected, node.Status, "node %s status", id)
}

func AssertProgress(t *testing.T, svc *service.NodeService, id string, expected float64) {
    t.Helper()
    node, err := svc.GetNode(context.Background(), id)
    require.NoError(t, err)
    assert.InDelta(t, expected, node.Progress, 0.001, "node %s progress", id)
}
```

---

## 5. Performance Testing

### 5.1 Benchmark Tests

```go
func BenchmarkCreateNode(b *testing.B) {
    store := benchutil.NewBenchStore(b)
    svc := service.NewNodeService(store, ...)

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := svc.CreateNode(ctx, &CreateNodeRequest{
            Title:   fmt.Sprintf("Node %d", i),
            Project: "BENCH",
        })
        if err != nil {
            b.Fatal(err)
        }
    }
}
```

### 5.2 NFR Performance Gates

Benchmarks MUST be run before release and compared against NFR targets:

```bash
go test ./... -bench=. -benchmem -count=5 | tee bench.txt
# Compare against targets in QUALITY-STANDARDS.md §3.7
```

---

## 6. Fuzz Testing

Critical input parsing code MUST have fuzz tests:

```go
func FuzzValidatePrefix(f *testing.F) {
    // Seed corpus
    f.Add("PROJ")
    f.Add("A")
    f.Add("")
    f.Add("proj")
    f.Add("ABCDEFGHIJKLMNOPQRSTU") // 21 chars

    f.Fuzz(func(t *testing.T, prefix string) {
        err := ValidatePrefix(prefix)
        if err == nil {
            // If validation passes, verify the prefix is actually valid
            assert.Regexp(t, `^[A-Z][A-Z0-9-]{0,19}$`, prefix)
        }
    })
}
```

Fuzz targets for mtix:
- `FuzzValidatePrefix` — Project prefix validation
- `FuzzParseDotNotationID` — ID parsing
- `FuzzDecomposeInput` — Decompose stdin format parsing
- `FuzzSearchQuery` — FTS5 query sanitization
- `FuzzJSONImport` — Export/import JSON parsing

---

## 7. Race Detection

All tests MUST pass with the Go race detector enabled:

```bash
go test ./... -race -count=1
```

This is non-negotiable. Race conditions in safety-critical software are unacceptable.

---

## 8. CI Pipeline Integration

```yaml
# Conceptual CI pipeline stages
stages:
  - lint:       golangci-lint run
  - test:       go test ./... -race -coverprofile=cover.out -count=1
  - coverage:   go tool cover -func=cover.out (verify ≥ 90%)
  - security:   govulncheck ./... && gosec ./...
  - benchmark:  go test ./... -bench=. -benchmem -count=5
  - e2e:        go test ./e2e/... -v -count=1
  - build:      go build -o mtix ./cmd/mtix/
```

Every stage MUST pass before code is considered complete.

---

*Write the test. Watch it fail. Write the code. Watch it pass. Clean it up. Repeat.*
