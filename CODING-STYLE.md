# mtix Coding Style & Patterns

**Version:** 1.0
**Date:** 2026-03-08
**Language:** Go 1.22+

---

## 1. Project Layout

mtix follows the standard Go project layout with clear separation of concerns:

```
mtix/
├── cmd/mtix/          # CLI entry points (one file per command group)
├── internal/          # Private application code
│   ├── model/         # Domain types (Node, Dependency, etc.)
│   ├── store/         # Storage interface + implementations
│   │   └── sqlite/    # SQLite-specific implementation
│   ├── service/       # Business logic orchestration
│   ├── api/           # API transports (HTTP, gRPC)
│   │   ├── http/      # REST + WebSocket handlers
│   │   └── grpc/      # gRPC service implementations
│   ├── mcp/           # MCP server + tools
│   ├── docs/          # Documentation generator
│   └── testutil/      # Shared test helpers
├── proto/             # Protobuf definitions
├── web/               # Frontend source (embedded in binary)
├── sdk/python/        # Python SDK
└── e2e/               # End-to-end tests
```

### 1.1 Package Naming Rules

- Package names MUST be lowercase, single-word, no underscores
- Package names MUST NOT repeat the directory structure (e.g., `store`, not `storepackage`)
- Internal packages MUST be under `internal/` to prevent external imports
- Test helper packages MUST be under `internal/testutil/`

---

## 2. Naming Conventions

### 2.1 General Rules

| Element | Convention | Example |
|---------|-----------|---------|
| Package | lowercase, single word | `store`, `model`, `service` |
| Exported type | PascalCase | `NodeService`, `SQLiteStore` |
| Unexported type | camelCase | `queryBuilder`, `configLoader` |
| Interface | Behavior-based, no "I" prefix | `Store`, `NodeCreator`, `EventBroadcaster` |
| Constants | PascalCase (exported), camelCase (unexported) | `MaxDepth`, `defaultTimeout` |
| Errors | `Err` prefix | `ErrNotFound`, `ErrInvalidTransition` |
| Test functions | `Test{Function}_{Scenario}_{Expected}` | `TestCreateNode_DuplicateID_ReturnsError` |
| Enum-like constants | Type + Value | `StatusOpen`, `StatusDone`, `DepTypeBlocks` |
| SQL column mapping | snake_case in DB, PascalCase in Go struct | `defer_until` → `DeferUntil` |

### 2.2 Error Variables

```go
// Package-level sentinel errors
var (
    ErrNotFound           = errors.New("not found")
    ErrAlreadyExists      = errors.New("already exists")
    ErrInvalidInput       = errors.New("invalid input")
    ErrInvalidTransition  = errors.New("invalid transition")
    ErrCycleDetected      = errors.New("cycle detected")
    ErrConflict           = errors.New("conflict")
    ErrAlreadyClaimed     = errors.New("already claimed")
    ErrNodeBlocked        = errors.New("node blocked")
    ErrStillDeferred      = errors.New("still deferred")
    ErrAgentStillActive   = errors.New("agent still active")
    ErrNoActiveSession    = errors.New("no active session")
    ErrInvalidConfigKey   = errors.New("invalid config key")
    ErrDepthWarning       = errors.New("depth warning")   // Advisory only (FR-1.1a) — operation proceeds, warning emitted
)
```

### 2.3 Domain Type Constants

```go
type Status string

const (
    StatusOpen        Status = "open"
    StatusInProgress  Status = "in_progress"
    StatusBlocked     Status = "blocked"
    StatusDone        Status = "done"
    StatusDeferred    Status = "deferred"
    StatusCancelled   Status = "cancelled"
    StatusInvalidated Status = "invalidated"
)

type DepType string

const (
    DepTypeBlocks        DepType = "blocks"
    DepTypeRelated       DepType = "related"
    DepTypeDiscoveredFrom DepType = "discovered_from"
    DepTypeDuplicates    DepType = "duplicates"
)
```

---

## 3. Architecture Patterns

### 3.1 Layered Architecture

```
CLI Commands / REST Handlers / gRPC Handlers / MCP Tools
                          │
                    Service Layer (business logic)
                          │
                    Store Interface (data access contract)
                          │
                    SQLite Implementation
```

**Rules:**
- CLI commands, HTTP handlers, gRPC handlers, and MCP tools MUST NOT access the store directly
- All business logic MUST live in the service layer
- The store layer MUST expose only data access operations — no business rules
- Cross-cutting concerns (logging, metrics) use middleware/interceptors

### 3.2 Store Interface Pattern

```go
// Store defines the data access contract.
// All implementations MUST be safe for concurrent use.
type Store interface {
    // Node operations
    CreateNode(ctx context.Context, node *model.Node) error
    GetNode(ctx context.Context, id string) (*model.Node, error)
    UpdateNode(ctx context.Context, id string, updates *model.NodeUpdate) error
    DeleteNode(ctx context.Context, id string, cascade bool, deletedBy string) error
    UndeleteNode(ctx context.Context, id string) error
    ListChildren(ctx context.Context, parentID string, opts ListOptions) ([]*model.Node, error)
    GetTree(ctx context.Context, id string, depth int) (*model.TreeNode, error)
    GetAncestors(ctx context.Context, id string) ([]*model.Node, error)

    // Queries
    ListNodes(ctx context.Context, filter NodeFilter, opts ListOptions) ([]*model.Node, int, error)
    ReadyNodes(ctx context.Context, under string, opts ListOptions) ([]*model.Node, error)
    BlockedNodes(ctx context.Context, opts ListOptions) ([]*model.Node, error)
    StaleNodes(ctx context.Context, hours int, opts ListOptions) ([]*model.Node, error)
    OrphanNodes(ctx context.Context, opts ListOptions) ([]*model.Node, error)
    SearchNodes(ctx context.Context, query string, filter NodeFilter, opts ListOptions) ([]*model.Node, error)

    // Dependencies
    CreateDependency(ctx context.Context, dep *model.Dependency) error
    DeleteDependency(ctx context.Context, fromID, toID string, depType model.DepType) error
    GetDependencies(ctx context.Context, nodeID string) (*model.DependencySet, error)
    DetectCycle(ctx context.Context, fromID, toID string) (bool, error)

    // Progress
    RecalculateProgress(ctx context.Context, nodeID string) error

    // Sequences
    NextSequence(ctx context.Context, project, parentPath string) (int, error)

    // Agents & Sessions
    UpsertAgent(ctx context.Context, agent *model.Agent) error
    GetAgent(ctx context.Context, agentID string) (*model.Agent, error)
    CreateSession(ctx context.Context, session *model.Session) error
    EndSession(ctx context.Context, agentID string) (*model.SessionSummary, error)
    GetActiveSession(ctx context.Context, agentID string) (*model.Session, error)

    // Maintenance
    RunGC(ctx context.Context, retentionDuration time.Duration) (int, error)
    RunIntegrityCheck(ctx context.Context) (*model.IntegrityResult, error)
    Backup(ctx context.Context, destPath string) error

    // Export/Import
    Export(ctx context.Context) (*model.ExportData, error)
    Import(ctx context.Context, data *model.ExportData, replace bool) error

    // Verify (diagnostics)
    Verify(ctx context.Context) (*model.VerifyResult, error)

    // Lifecycle
    Close() error
}
```

### 3.3 Service Layer Pattern

```go
type NodeService struct {
    store       Store
    broadcaster EventBroadcaster
    config      *Config
    logger      *slog.Logger
    clock       func() time.Time  // Injected clock — NEVER use time.Now() directly
}

// NewNodeService creates a new NodeService with all dependencies injected.
// The clock parameter enables deterministic testing of all time-dependent behavior.
func NewNodeService(store Store, broadcaster EventBroadcaster, config *Config, logger *slog.Logger, clock func() time.Time) *NodeService {
    return &NodeService{
        store:       store,
        broadcaster: broadcaster,
        config:      config,
        logger:      logger,
        clock:       clock,
    }
}
```

### 3.4 Dependency Injection

- ALL dependencies MUST be injected via constructor functions
- Global state is FORBIDDEN
- Singletons are FORBIDDEN
- The `main.go` is the only place where dependencies are wired together

### 3.5 Context Propagation

- ALL operations MUST accept `context.Context` as the first parameter
- Context MUST carry request-scoped values (agent ID, session ID)
- Context cancellation MUST be respected in long-running operations

---

## 4. Error Handling

### 4.1 Error Wrapping

```go
// CORRECT: Wrap with context
if err := s.store.GetNode(ctx, id); err != nil {
    return fmt.Errorf("get node %s: %w", id, err)
}

// WRONG: Lost context
if err := s.store.GetNode(ctx, id); err != nil {
    return err
}

// WRONG: String formatting loses error chain
if err := s.store.GetNode(ctx, id); err != nil {
    return fmt.Errorf("failed: %s", err)
}
```

### 4.2 Error Checking

- ALL errors MUST be checked — no exceptions
- `errcheck` linter enforces this
- Errors from `defer` closers MUST be handled (use named returns or helper)

```go
// Handle defer errors correctly
func (s *SQLiteStore) withTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
    tx, err := s.writeDB.BeginTx(ctx, nil)
    if err != nil {
        return fmt.Errorf("begin transaction: %w", err)
    }
    defer func() {
        if p := recover(); p != nil {
            _ = tx.Rollback()
            panic(p)
        }
        if err != nil {
            if rbErr := tx.Rollback(); rbErr != nil {
                err = fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
            }
        }
    }()
    err = fn(tx)
    if err != nil {
        return err
    }
    return tx.Commit()
}
```

### 4.3 Sentinel Errors for API Responses

```go
// Map sentinel errors to HTTP/gRPC status codes
func mapError(err error) (code string, httpStatus int) {
    switch {
    case errors.Is(err, ErrNotFound):
        return "NOT_FOUND", http.StatusNotFound
    case errors.Is(err, ErrAlreadyExists):
        return "ALREADY_EXISTS", http.StatusConflict
    case errors.Is(err, ErrInvalidInput):
        return "INVALID_INPUT", http.StatusBadRequest
    case errors.Is(err, ErrInvalidTransition):
        return "INVALID_TRANSITION", http.StatusConflict
    case errors.Is(err, ErrCycleDetected):
        return "CYCLE_DETECTED", http.StatusConflict
    default:
        return "INTERNAL", http.StatusInternalServerError
    }
}
```

---

## 5. SQL Patterns

### 5.1 Parameterized Queries Only (NFR-5.8)

```go
// CORRECT: Parameterized query
row := tx.QueryRowContext(ctx, "SELECT * FROM nodes WHERE id = ?", id)

// CORRECT: LIKE with parameterized pattern
rows, err := db.QueryContext(ctx,
    `SELECT * FROM nodes WHERE id LIKE ? ESCAPE '\' AND deleted_at IS NULL`,
    parentID+".%",
)

// FORBIDDEN — string concatenation
row := tx.QueryRowContext(ctx, "SELECT * FROM nodes WHERE id = '"+id+"'")

// FORBIDDEN — fmt.Sprintf for SQL
query := fmt.Sprintf("SELECT * FROM nodes WHERE project = '%s'", project)
```

### 5.2 Transaction Pattern

```go
// All writes go through the withTx helper
// NOTE: Timestamps use the injected clock (s.clock) — never time.Now() directly.
func (s *SQLiteStore) CreateNode(ctx context.Context, node *model.Node) error {
    return s.withTx(ctx, func(tx *sql.Tx) error {
        // Generate ID using atomic sequence increment
        seq, err := s.nextSequenceTx(tx, node.Project, node.ParentID)
        if err != nil {
            return fmt.Errorf("next sequence: %w", err)
        }

        node.ID = buildDotNotationID(node.ParentID, node.Project, seq)
        node.Seq = seq
        node.CreatedAt = s.clock().UTC()
        node.UpdatedAt = node.CreatedAt

        _, err = tx.ExecContext(ctx,
            `INSERT INTO nodes (id, parent_id, depth, seq, project, title, ...)
             VALUES (?, ?, ?, ?, ?, ?, ...)`,
            node.ID, node.ParentID, node.Depth, node.Seq, node.Project, node.Title,
        )
        if err != nil {
            return fmt.Errorf("insert node: %w", err)
        }

        // Recalculate parent progress in same transaction
        if node.ParentID != "" {
            if err := s.recalculateProgressTx(ctx, tx, node.ParentID); err != nil {
                return fmt.Errorf("recalculate progress: %w", err)
            }
        }

        return nil
    })
}
```

### 5.3 Connection Management

```go
// Separate read and write connection pools
type SQLiteStore struct {
    writeDB *sql.DB // MaxOpenConns(1) — serialized writes
    readDB  *sql.DB // Multiple read connections
    dbPath  string
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
    writeDB, err := sql.Open("sqlite", dbPath)
    if err != nil {
        return nil, fmt.Errorf("open write db: %w", err)
    }
    writeDB.SetMaxOpenConns(1)

    // PRAGMA setup on EVERY connection
    writeDB.SetConnMaxLifetime(0) // Connections live forever
    if _, err := writeDB.Exec(`
        PRAGMA journal_mode = WAL;
        PRAGMA busy_timeout = 5000;
        PRAGMA foreign_keys = ON;
    `); err != nil {
        return nil, fmt.Errorf("write db pragmas: %w", err)
    }

    readDB, err := sql.Open("sqlite", dbPath+"?mode=ro")
    if err != nil {
        return nil, fmt.Errorf("open read db: %w", err)
    }
    // Read connections also need foreign_keys for consistency
    readDB.SetConnMaxLifetime(0)
    if _, err := readDB.Exec("PRAGMA foreign_keys = ON;"); err != nil {
        return nil, fmt.Errorf("read db pragmas: %w", err)
    }

    return &SQLiteStore{writeDB: writeDB, readDB: readDB, dbPath: dbPath}, nil
}
```

---

## 6. Concurrency Patterns

### 6.1 No Global Mutex

SQLite's WAL mode + single write connection handles serialization. Do NOT add application-level mutexes for database access.

### 6.2 Context Cancellation

```go
func (s *NodeService) GetTree(ctx context.Context, id string, depth int) (*model.TreeNode, error) {
    // Check context before expensive operation
    select {
    case <-ctx.Done():
        return nil, ctx.Err()
    default:
    }
    return s.store.GetTree(ctx, id, depth)
}
```

### 6.3 Goroutine Lifecycle

- All background goroutines MUST be started via `errgroup` or similar pattern
- All goroutines MUST respect context cancellation
- All goroutines MUST be cleanly stopped during graceful shutdown

---

## 7. Logging

### 7.1 Structured Logging with slog

```go
// CORRECT: Structured logging
s.logger.Info("node created",
    slog.String("id", node.ID),
    slog.String("project", node.Project),
    slog.Int("depth", node.Depth),
)

// CORRECT: Error logging with context
s.logger.Error("failed to create node",
    slog.String("id", node.ID),
    slog.Any("error", err),
)

// WRONG: Printf-style logging
log.Printf("Created node %s in project %s", node.ID, node.Project)
```

### 7.2 Log Levels

| Level | Usage |
|-------|-------|
| `Debug` | Internal state, SQL queries, detailed flow |
| `Info` | Operations completed (node created, session started) |
| `Warn` | Recoverable issues (stale agent, depth warning) |
| `Error` | Unrecoverable issues (DB corruption, panic recovery) |

---

## 8. JSON Handling

### 8.1 Canonical JSON

- Use `encoding/json` (stdlib) for all JSON operations
- JSON field names MUST use `snake_case` (matching REST API convention)
- Struct tags: `json:"field_name"` for required, `json:"field_name,omitempty"` for optional

### 8.2 Timestamp Format

All timestamps MUST be ISO-8601 UTC: `2026-03-08T12:34:56Z`

```go
type Node struct {
    ID        string    `json:"id"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    ClosedAt  *time.Time `json:"closed_at,omitempty"`
}
```

---

## 9. File Organization Rules

### 9.1 One Concept Per File

- Each file should contain a single concept (one struct + its methods, or one interface)
- Maximum file length: 500 lines (excluding tests)
- If a file exceeds 500 lines, it MUST be split

### 9.2 Test File Location

- Unit tests: same directory as source, `*_test.go`
- Integration tests: `internal/integration/`
- E2E tests: `e2e/`
- Test fixtures: `internal/testutil/fixtures/`

---

## 10. Forbidden Patterns

The following patterns are FORBIDDEN in mtix:

1. **Global variables** (except sentinel errors and constants)
2. **init() functions** (except for driver registration in `main.go`)
3. **Panic for error handling** (panic only for truly unrecoverable states)
4. **String concatenation for SQL**
5. **Ignoring errors** (even in defer)
6. **Raw HTTP handlers** (use the chosen framework)
7. **Reflect-based magic** (except in test helpers)
8. **Build tags for feature flags** (use config instead)
9. **Circular imports** (enforce via architecture layers)
10. **time.Now() in production code** (inject a clock for testability)

### 10.1 Clock Injection for Testability

> **Note:** This is a simplified illustration of the clock injection concept.
> The canonical `NodeService` struct with all fields is in §3.3.

```go
// CORRECT: Inject clock (simplified — see §3.3 for full struct)
type NodeService struct {
    store Store
    clock func() time.Time
    // ... other fields (broadcaster, config, logger) omitted for clarity
}

func NewNodeService(store Store, clock func() time.Time) *NodeService {
    return &NodeService{store: store, clock: clock}
}

// In production (main.go):
svc := NewNodeService(realStore, time.Now)

// In tests:
fixedTime := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
svc := NewNodeService(mockStore, func() time.Time { return fixedTime })

// WRONG: Direct time.Now() call
node.CreatedAt = time.Now()
```

---

*Consistency is the last refuge of the unimaginative. But in safety-critical software, it's the first defense against bugs.*
