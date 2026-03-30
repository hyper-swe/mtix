# mtix Quality Standards

**Version:** 1.0
**Date:** 2026-03-08
**Classification:** Safety-Critical Software Development Standards
**Applicable Standards:** DO-178C (Airborne), IEC 62304 (Medical), NASA-STD-8739.8, MIL-STD-498, OWASP ASVS

---

## 1. Quality Philosophy

mtix is developed to safety-critical standards appropriate for use in environments where software failures have significant consequences: airline operations, hospital systems, NASA missions, and Department of Defense applications. Every line of code, every test, every decision must reflect this standard.

**Core Principle:** "If it's not tested, it doesn't work. If it's not documented, it doesn't exist."

---

## 2. Code Coverage Requirements

### 2.1 Minimum Coverage Thresholds

| Layer | Line Coverage | Branch Coverage | Function Coverage |
|-------|--------------|-----------------|-------------------|
| Storage (SQLite) | 95% | 90% | 100% |
| Service (Business Logic) | 95% | 90% | 100% |
| Model (Data Types) | 90% | 85% | 100% |
| CLI Commands | 90% | 85% | 100% |
| REST API Handlers | 90% | 85% | 100% |
| gRPC Handlers | 90% | 85% | 100% |
| MCP Tools | 90% | 85% | 100% |
| WebSocket | 85% | 80% | 100% |
| Overall Project | 90% | 85% | 100% |

### 2.2 Release Coverage Gates

Release pipelines enforce coverage thresholds based on release type:

| Release Type | Tag Pattern | Overall Minimum |
|-------------|-------------|-----------------|
| Beta | `v*.*.*-beta*`, `v*.*.*-rc*` | 85% |
| GA (General Availability) | `v*.*.*` (clean semver) | 90% |

Beta releases allow shipping while coverage is raised across packages. GA releases enforce the full per-layer thresholds above.

### 2.3 Coverage Enforcement

- **Line coverage** is measured on every commit using `go test -coverprofile` and `go tool cover -func`
- **Branch coverage** is verified via two complementary mechanisms:
  1. **Table-driven test exhaustiveness:** Every conditional (`if`, `switch`, `select`) MUST have table-driven test cases covering both/all branches. Code reviewers MUST verify this during review (see §7.1 checklist).
  2. **Exhaustive state machine testing:** All `switch` statements on `Status`, `DepType`, `IssueType`, and other enums MUST have tests for every case plus the default/invalid case. The `TestStateMachine_AllValidTransitions` and `TestStateMachine_AllInvalidTransitions` tests serve as the canonical example.
  - **Note:** Go's built-in `go test -coverprofile` measures statement (line) coverage only, NOT branch coverage. True branch/MC/DC coverage measurement requires third-party tools or manual inspection. The branch coverage percentages in the table above are targets enforced via code review and test completeness checks, not via automated tooling. If a Go branch coverage tool matures (e.g., `gobco`), it SHOULD be added to the CI pipeline.
- Coverage drops below threshold MUST fail the CI pipeline
- New code MUST have ≥90% line coverage — no exceptions
- Coverage exclusions (build tags, generated code) MUST be documented and approved

### 2.3 Coverage Exclusions (Approved)

The following are excluded from coverage calculations:
- Auto-generated protobuf code (`*.pb.go`)
- Embedded web UI static assets
- `main.go` entry point (minimal bootstrap only)
- Build/version info injection

---

## 3. Testing Requirements

### 3.1 Test-Driven Development (TDD) Mandate

All production code MUST be developed using strict TDD:

1. **Red:** Write a failing test that defines the expected behavior
2. **Green:** Write the minimum code to make the test pass
3. **Refactor:** Clean up the code while keeping tests green

No production code may be written without a corresponding test written first.

### 3.2 Test Categories

| Category | Purpose | Location | Naming |
|----------|---------|----------|--------|
| Unit Tests | Test individual functions/methods in isolation | `*_test.go` alongside source | `Test{Function}_{Scenario}` |
| Integration Tests | Test component interactions (e.g., store + service) | `internal/integration/` | `TestIntegration_{Flow}` |
| End-to-End Tests | Test CLI commands and API endpoints | `e2e/` | `TestE2E_{Workflow}` |
| Property Tests | Verify invariants with random inputs | `*_prop_test.go` | `TestProp_{Property}` |
| Fuzz Tests | Discover edge cases via fuzzing | `*_fuzz_test.go` | `Fuzz{Function}` |
| Benchmark Tests | Performance verification | `*_bench_test.go` | `Benchmark{Operation}` |
| Concurrency Tests | Race condition detection | `*_race_test.go` | `TestRace_{Scenario}` |

### 3.3 Test Naming Convention

```go
func TestCreateNode_WithValidInput_ReturnsNode(t *testing.T) {}
func TestCreateNode_WithDuplicateID_ReturnsAlreadyExists(t *testing.T) {}
func TestCreateNode_UnderCancelledParent_ReturnsInvalidInput(t *testing.T) {}
func TestStateMachine_DoneOnBlocked_ReturnsInvalidTransition(t *testing.T) {}
```

Format: `Test{Function}_{Condition}_{ExpectedResult}`

### 3.4 Test Independence

- Each test MUST be independently runnable
- Tests MUST NOT depend on execution order
- Each test MUST set up its own fixtures and clean up after itself
- Shared test helpers MUST be in `internal/testutil/`
- Database tests MUST use isolated SQLite instances (temp file per test via `t.TempDir()`, auto-cleaned). Temp files are required because the dual read/write connection pool pattern (CODING-STYLE.md §5.3) does not work with `:memory:` databases.

### 3.5 Table-Driven Tests

All tests with multiple input/output scenarios MUST use Go's table-driven test pattern:

```go
func TestValidatePrefix(t *testing.T) {
    tests := []struct {
        name    string
        prefix  string
        wantErr bool
    }{
        {"valid simple", "PROJ", false},
        {"valid with numbers", "PROJ2", false},
        {"invalid lowercase", "proj", true},
        {"invalid starts with number", "1PROJ", true},
        {"invalid too long", "ABCDEFGHIJKLMNOPQRSTU", true},
        {"invalid special chars", "PROJ%", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := ValidatePrefix(tt.prefix)
            if (err != nil) != tt.wantErr {
                t.Errorf("ValidatePrefix(%q) error = %v, wantErr %v", tt.prefix, err, tt.wantErr)
            }
        })
    }
}
```

### 3.6 Safety-Critical Test Scenarios

The following scenarios MUST have dedicated test coverage:

1. **State Machine Exhaustive Testing:** Every valid transition and every invalid transition must be tested
2. **Concurrent Access:** Multiple goroutines performing simultaneous writes
3. **Progress Rollup Determinism:** Same inputs always produce same outputs regardless of order
4. **Backup/Export Integrity:** Verify checksum and node count on every export/import cycle
5. **Startup Integrity Check:** Corrupt database correctly detected and rejected
6. **Idempotent Operations:** Duplicate requests produce identical results
7. **Soft-Delete Safety Chain:** Invalidate-before-delete ordering verified
8. **Circular Dependency Detection:** All graph topologies tested
9. **FTS Consistency:** Search index matches node table after all operation types
10. **Graceful Shutdown:** In-flight requests complete, WAL checkpointed

### 3.7 Performance Benchmarks (NFR Validation)

| Benchmark | Target | Test |
|-----------|--------|------|
| Node creation (local) | <10ms | `BenchmarkCreateNode` |
| Tree retrieval (1000 nodes) | <100ms | `BenchmarkGetTree_1000` |
| Progress rollup (depth 10) | <50ms | `BenchmarkProgressRollup_Depth10` |
| Progress rollup (depth 50) | <250ms | `BenchmarkProgressRollup_Depth50` |
| CLI startup + simple command | <200ms | `BenchmarkCLI_Show` |
| FTS search (10K nodes) | <200ms | `BenchmarkFTSSearch_10K` |

> **Note:** No explicit NFR target exists for FTS performance beyond 10K nodes (100K+ scale). Monitor during Phase 5 performance optimization. If degradation is observed, consider FTS5 custom tokenizers and relevance ranking tuning per REQUIREMENTS.md §7 Phase 5.

---

## 4. Static Analysis Requirements

### 4.1 Required Analysis Tools

| Tool | Purpose | Configuration |
|------|---------|---------------|
| `golangci-lint` | Comprehensive Go linter | `.golangci.yml` (strict config) |
| `gosec` | Security vulnerability scanner | All rules enabled |
| `go vet` | Go compiler analysis | Default |
| `staticcheck` | Advanced static analysis | All checks enabled |
| `errcheck` | Unchecked error detection | No exclusions |
| `gocritic` | Code quality suggestions | All checks enabled |
| `ineffassign` | Unused assignment detection | Default |
| `misspell` | Typo detection | Default |

### 4.2 Zero-Warning Policy

- All static analysis warnings MUST be resolved before merge
- No lint suppression comments (`//nolint`) without documented justification
- Each `//nolint` MUST include the specific linter name and a reason

### 4.3 Cyclomatic Complexity Limits

- Maximum cyclomatic complexity per function: 15
- Maximum cognitive complexity per function: 20
- Functions exceeding these limits MUST be refactored

---

## 5. Security Requirements

### 5.1 SQL Injection Prevention (NFR-5.8)

- ALL SQL queries MUST use parameterized queries (`?` placeholders)
- String concatenation for SQL construction is FORBIDDEN — zero exceptions
- A custom linter rule MUST detect SQL string concatenation patterns
- All SQL queries MUST be reviewed for injection vulnerabilities

### 5.2 Input Validation

- ALL external inputs (CLI args, REST body, gRPC params, MCP tool params) MUST be validated
- Validation MUST occur at the boundary (handler/command level), not deep in business logic
- Invalid inputs MUST return descriptive error messages
- Input length limits MUST be enforced (title: 500 chars, description: 50KB, prompt: 100KB)

### 5.3 Dependency Security

- All dependencies MUST be from the approved package list (APPROVED-PACKAGES.md)
- New dependencies MUST go through the approval process (PACKAGE-APPROVAL-PROCESS.md)
- `go.sum` MUST be committed and verified
- Known CVEs in dependencies MUST be tracked and patched within 72 hours

### 5.4 OWASP Compliance

mtix MUST comply with OWASP ASVS Level 2 for the following categories:
- V5: Validation, Sanitization, and Encoding
- V7: Error Handling and Logging
- V8: Data Protection
- V12: Files and Resources
- V14: Configuration

---

## 6. Documentation Requirements

### 6.1 Code Documentation

- All exported types, functions, and methods MUST have godoc comments
- Complex algorithms MUST include inline comments explaining the approach
- All SQL queries MUST have comments explaining what they do and why
- Error handling paths MUST be documented with rationale

### 6.2 Architecture Decision Records (ADRs)

Major technical decisions MUST be documented as ADRs:
- ADR-001: Storage Engine (already exists)
- Every new significant decision gets a new ADR
- ADRs are immutable once approved — superseded ADRs link to their replacement

### 6.3 Test Documentation

- Each test file MUST include a package-level comment explaining what it tests
- Complex test scenarios MUST include comments explaining the setup and assertions
- Property-based tests MUST document the invariant being verified

---

## 7. Code Review Requirements

### 7.1 Review Checklist

Every code change MUST be verified against:

- [ ] Tests written before code (TDD compliance)
- [ ] All new code has ≥90% coverage
- [ ] No `//nolint` without justification
- [ ] All SQL uses parameterized queries
- [ ] All inputs validated at boundaries
- [ ] Error handling complete — no swallowed errors
- [ ] Godoc comments on all exports
- [ ] No unapproved dependencies added
- [ ] Benchmark targets met (if performance-critical path)
- [ ] State machine transitions verified against FR-3.5
- [ ] Activity stream entries created for all mutations

### 7.2 Critical Path Review

Changes to the following areas require enhanced review:
- State machine transitions (FR-3.5)
- Progress rollup (FR-5.7)
- SQLite schema or queries
- Security controls (NFR-5.*)
- MCP tool definitions
- Export/import integrity checks
- Concurrency model (connection pools, PID lock)

---

## 8. Build and Release Requirements

### 8.1 Reproducible Builds

- Builds MUST be reproducible — same source MUST produce identical binary
- Build flags MUST include version, commit hash, and build timestamp
- `go.sum` MUST be verified during build

### 8.2 Release Checklist

- [ ] All tests pass (unit, integration, e2e)
- [ ] Coverage thresholds met
- [ ] Static analysis clean
- [ ] Security scan clean
- [ ] Benchmark targets verified
- [ ] Schema migration tested (upgrade from previous version)
- [ ] `mtix verify` passes on test databases
- [ ] Documentation regenerated (`mtix docs generate`)
- [ ] CHANGELOG updated

---

## 9. Incident Response

### 9.1 Data Integrity Issues

If a data integrity issue is discovered in production:
1. Immediately document the issue
2. Run `mtix verify` to assess scope
3. Restore from backup if needed
4. Create a post-mortem with root cause analysis
5. Add regression tests
6. Update this document if standards need strengthening

### 9.2 Security Vulnerabilities

If a security vulnerability is discovered:
1. Assess severity using CVSS scoring
2. Critical/High: patch within 24 hours
3. Medium: patch within 72 hours
4. Low: patch in next scheduled release
5. Document in security advisory
6. Verify fix with specific test cases

---

*Quality is not an act, it is a habit. — Aristotle*
