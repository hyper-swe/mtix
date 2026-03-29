# Approved Third-Party Packages

**Version:** 1.0
**Date:** 2026-03-08
**Last Security Audit:** 2026-03-08
**Go Version:** 1.22+

---

## Approval Status Legend

| Status | Meaning |
|--------|---------|
| APPROVED | Fully audited, no blocking issues |
| APPROVED-CONDITIONAL | Approved with specific version/config requirements |
| UNDER-REVIEW | Pending approval — do NOT use yet |

---

## Core Runtime Dependencies

### 1. SQLite Driver

| Package | `modernc.org/sqlite` |
|---------|---------------------|
| Version | ≥ 1.35.0 (tracks SQLite 3.51.2+) |
| License | BSD-3-Clause |
| Purpose | Pure Go SQLite driver — no CGO (NFR-2.1, NFR-4.4) |
| Status | **APPROVED** |
| CVE Status | Inherits upstream SQLite CVEs. CVE-2025-6965 (buffer overflow), CVE-2025-47914 (concat_ws), CVE-2025-58181 (db_config DoS) all fixed in SQLite 3.50.2+. Ensure modernc.org/sqlite tracks the latest SQLite version. |
| Notes | This is the ONLY approved SQLite driver. Do NOT use `mattn/go-sqlite3` (requires CGO). The pure-Go implementation satisfies NFR-4.7 (single binary, no external deps). |
| Alternatives Rejected | `mattn/go-sqlite3` (CGO dependency violates NFR-4.7), `crawshaw.io/sqlite` (less maintained) |

### 2. CLI Framework

| Package | `github.com/spf13/cobra` |
|---------|--------------------------|
| Version | ≥ 1.8.1 |
| License | Apache 2.0 |
| Purpose | CLI command framework (NFR-4.2) |
| Status | **APPROVED** |
| CVE Status | No direct CVEs. Historical transitive dependency CVEs resolved in current versions. |
| Notes | Industry standard for Go CLIs. Used by kubectl, Hugo, Docker CLI. |

### 3. Configuration

| Package | `github.com/spf13/viper` |
|---------|--------------------------|
| Version | ≥ 1.20.1 |
| License | MIT |
| Purpose | YAML configuration management (FR-11) |
| Status | **APPROVED-CONDITIONAL** |
| CVE Status | CVE-2022-29153 (SSRF in Consul provider — not used by mtix). Ensure Consul/etcd remote providers are NOT imported. |
| Conditions | MUST NOT enable remote configuration providers (Consul, etcd). mtix uses local YAML files only. Import only `github.com/spf13/viper` — do NOT import provider sub-packages. |
| Notes | Tightly coupled with Cobra for CLI config binding. |

### 4. HTTP Framework

| Package | `github.com/gin-gonic/gin` |
|---------|---------------------------|
| Version | ≥ 1.12.0 |
| License | MIT |
| Purpose | REST API server (NFR-4.3) |
| Status | **APPROVED-CONDITIONAL** |
| CVE Status | CVE-2020-28483 (X-Forwarded-For spoofing — mitigated by localhost-only binding). CVE-2023-29401 (filename sanitization — fixed in 1.9.1+). |
| Conditions | MUST configure `gin.SetTrustedProxies(nil)` when binding to localhost. MUST NOT use `c.ClientIP()` for security decisions when exposed to network. |
| Notes | If Echo is preferred, substitute `github.com/labstack/echo/v4` (also approved). Do NOT use both. |

**Alternative (also approved):**

| Package | `github.com/labstack/echo/v4` |
|---------|-------------------------------|
| Version | ≥ 4.13.4 |
| License | MIT |
| Purpose | REST API server alternative (NFR-4.3) |
| Status | **APPROVED-CONDITIONAL** |
| CVE Status | No CVEs in v4 series. v4 support ends 2026-12-31; plan migration to v5 before then. |
| Conditions | v4 only. v5 not yet approved (AIKIDO-2026-10148 directory listing issue). |

**Decision:** Choose ONE HTTP framework. The choice MUST be recorded in an ADR.

### 5. WebSocket

| Package | `github.com/gorilla/websocket` |
|---------|-------------------------------|
| Version | ≥ 1.5.3 |
| License | BSD-2-Clause |
| Purpose | WebSocket event streaming (FR-7.5) |
| Status | **APPROVED** |
| CVE Status | CVE-2020-27813 (integer overflow DoS) fixed in 1.4.1+. Current version clean. |
| Notes | De facto standard for Go WebSocket. Well-maintained despite gorilla org transition. |

### 6. gRPC

| Package | `google.golang.org/grpc` |
|---------|--------------------------|
| Version | ≥ 1.65.4 |
| License | Apache 2.0 |
| Purpose | gRPC server and client (FR-8) |
| Status | **APPROVED-CONDITIONAL** |
| CVE Status | CVE-2023-44487 (HTTP/2 Rapid Reset) fixed in 1.59.5+. HPACK table poisoning fixed in 1.65.4+. Private tokens leakage fixed in 1.64.2+. |
| Conditions | MUST use version ≥ 1.65.4 to include all security patches. |

### 7. Protocol Buffers

| Package | `google.golang.org/protobuf` |
|---------|-------------------------------|
| Version | ≥ 1.36.10 |
| License | BSD-3-Clause |
| Purpose | Protobuf serialization (FR-8, NFR-4.5) |
| Status | **APPROVED** |
| CVE Status | CVE-2023-24535 (OOB read) fixed in 1.29.1+. CVE-2024-24786 (infinite loop) fixed in 1.33.0+. Current version clean. |

---

## Development & Testing Dependencies

### 8. Test Assertions

| Package | `github.com/stretchr/testify` |
|---------|-------------------------------|
| Version | ≥ 1.10.0 |
| License | MIT |
| Purpose | Test assertions and mocking |
| Status | **APPROVED** |
| CVE Status | No known CVEs. |
| Sub-packages | `assert`, `require`, `mock`, `suite` all approved. |

### 9. ULID Generation

| Package | `github.com/oklog/ulid/v2` |
|---------|---------------------------|
| Version | ≥ 2.1.0 |
| License | Apache 2.0 |
| Purpose | ULID generation for activity entries and annotations (FR-3.4, FR-3.6) |
| Status | **APPROVED** |
| CVE Status | No known CVEs. |
| Notes | Preferred over UUID for sortability. Cryptographically secure random component. |

---

## Standard Library Extended Packages

### 10. Sync Utilities

| Package | `golang.org/x/sync` |
|---------|---------------------|
| Version | Latest |
| License | BSD-3-Clause |
| Purpose | `errgroup` for goroutine lifecycle management |
| Status | **APPROVED** |
| CVE Status | No known CVEs. |
| Notes | Be aware of `errgroup.WithContext` cancellation behavior — context is cancelled when `Wait()` returns. |

### 11. Crypto

| Package | `golang.org/x/crypto` |
|---------|----------------------|
| Version | Latest (track Go releases) |
| License | BSD-3-Clause |
| Purpose | SHA-256 for content hash (FR-3.7) and export checksum (FR-7.8) |
| Status | **APPROVED** |
| CVE Status | Historical CVEs addressed in current versions. Always use latest. |
| Notes | For SHA-256, prefer `crypto/sha256` from stdlib. Only use `x/crypto` if additional algorithms are needed. |

---

## Logging

### 12. Structured Logging

| Package | `log/slog` (stdlib) |
|---------|---------------------|
| Version | Go 1.21+ (stdlib) |
| License | BSD-3-Clause (Go license) |
| Purpose | Structured logging |
| Status | **APPROVED** (PREFERRED) |
| CVE Status | No CVEs (stdlib). |
| Notes | PREFERRED over third-party loggers. Built-in to Go stdlib, zero external dependency. Supports JSON output, log levels, and structured key-value pairs. |

**Alternative (also approved if slog insufficient):**

| Package | `github.com/rs/zerolog` |
|---------|------------------------|
| Version | Latest |
| License | MIT |
| Purpose | Zero-allocation structured logging |
| Status | **APPROVED** |
| CVE Status | No known CVEs. |
| Notes | Only use if `slog` performance is insufficient (unlikely for mtix workload). |

**Decision:** Use `log/slog` unless benchmarks show it's a bottleneck.

---

## NOT Approved — Explicitly Rejected

| Package | Reason |
|---------|--------|
| `mattn/go-sqlite3` | Requires CGO — violates NFR-4.7 (single binary) |
| `gorm.io/gorm` | ORM adds abstraction that conflicts with NFR-5.8 (parameterized query control) |
| `github.com/jmoiron/sqlx` | Acceptable quality but raw `database/sql` with parameterized queries gives full control per NFR-5.8 |
| `github.com/dgrijalva/jwt-go` | Deprecated, replaced by `golang-jwt/jwt` |
| `github.com/sirupsen/logrus` | Deprecated in favor of slog/zerolog |
| `github.com/pkg/errors` | Deprecated — use `fmt.Errorf` with `%w` verb |

---

## Dependency Compatibility Matrix

All approved packages have been verified for mutual compatibility:

| Dependency A | Dependency B | Conflict? | Notes |
|-------------|-------------|-----------|-------|
| modernc.org/sqlite | gin/echo | No | Independent — SQLite is storage, gin/echo is HTTP |
| cobra | viper | No | Designed to work together |
| gin | gorilla/websocket | No | WebSocket upgrades via gin middleware |
| grpc | protobuf | No | protobuf is a required dependency of grpc |
| testify | any | No | Test-only dependency, no runtime impact |
| ulid/v2 | any | No | Zero external dependencies |
| slog | any | No | Stdlib — no dependency conflicts possible |

**No conflicts detected among approved packages.**

---

## Security Monitoring

- **Automated:** `govulncheck` MUST be run weekly and before each release
- **Manual:** This document MUST be reviewed and updated quarterly
- **CVE Response:** See QUALITY-STANDARDS.md §9.2 for response timelines

```bash
# Run vulnerability check
go install golang.org/x/vuln/cmd/govulncheck@latest
govulncheck ./...
```

---

*Last full audit: 2026-03-10. Next scheduled audit: 2026-06-10.*

---

## Web UI (Frontend) Approved Packages

The web UI is a Vite + React + TypeScript SPA embedded in the Go binary via `//go:embed`. The same approval discipline applies to frontend dependencies.

### Production Dependencies

| Package | Version | License | Purpose | Status |
|---------|---------|---------|---------|--------|
| `react` | ^18.3.1 | MIT | UI component library | **APPROVED** |
| `react-dom` | ^18.3.1 | MIT | React DOM renderer | **APPROVED** |

### Development Dependencies

| Package | Version | License | Purpose | Status |
|---------|---------|---------|---------|--------|
| `vite` | ^6.2.0 | MIT | Build tool and dev server | **APPROVED** |
| `@vitejs/plugin-react` | ^4.3.4 | MIT | React Fast Refresh for Vite | **APPROVED** |
| `typescript` | ^5.7.2 | Apache 2.0 | Type-safe JavaScript | **APPROVED** |
| `vitest` | ^3.2.0 | MIT | Test runner (Vite-native) | **APPROVED** |
| `@vitest/coverage-v8` | ^3.2.0 | MIT | V8 code coverage for Vitest | **APPROVED** |
| `@testing-library/react` | ^16.1.0 | MIT | React component testing utilities | **APPROVED** |
| `@testing-library/jest-dom` | ^6.6.3 | MIT | DOM assertion matchers | **APPROVED** |
| `jsdom` | ^25.0.1 | MIT | DOM simulation for tests | **APPROVED** |
| `tailwindcss` | ^3.4.16 | MIT | Utility-first CSS framework | **APPROVED** |
| `postcss` | ^8.4.49 | MIT | CSS processing (Tailwind dependency) | **APPROVED** |
| `autoprefixer` | ^10.4.20 | MIT | CSS vendor prefixing | **APPROVED** |
| `eslint` | ^9.15.0 | MIT | JavaScript/TypeScript linter | **APPROVED** |
| `@typescript-eslint/eslint-plugin` | ^8.15.0 | MIT | TypeScript ESLint rules | **APPROVED** |
| `@typescript-eslint/parser` | ^8.15.0 | BSD-2 | TypeScript ESLint parser | **APPROVED** |
| `eslint-plugin-react-hooks` | ^5.0.0 | MIT | React hooks linting | **APPROVED** |
| `eslint-plugin-react-refresh` | ^0.4.14 | MIT | React Refresh linting | **APPROVED** |
| `@types/react` | ^18.3.12 | MIT | React type definitions | **APPROVED** |
| `@types/react-dom` | ^18.3.1 | MIT | React DOM type definitions | **APPROVED** |
| `@types/node` | ^25.4.0 | MIT | Node.js type definitions | **APPROVED** |

### Web UI — NOT Approved (Explicitly Rejected)

| Package | Reason |
|---------|--------|
| `axios` | Native `fetch` API sufficient; unnecessary dependency surface |
| `styled-components` / `emotion` | Tailwind CSS + CSS custom properties preferred |
| `redux` / `zustand` / `jotai` | React `useState` + context sufficient for current scope |
| `react-router` / `@tanstack/router` | Single-page app with simple view switching; no URL routing needed |
| `chart.js` / `recharts` / `d3` | SVG/CSS charts sufficient for dashboard; avoid large bundles |
| `moment` / `dayjs` | Use native `Intl.DateTimeFormat` and `Date` APIs |
| `lodash` | Use native JS array/object methods |
| `dompurify` | Custom `SafeMarkdown` component with allowlist approach preferred |

### Web UI CVE Status (2026-03-10)

| Advisory | Severity | Package | Status |
|----------|----------|---------|--------|
| GHSA-67mh-4wv8-2f99 | Moderate | `esbuild` ≤0.24.2 | **FIXED** — Upgrade `vite` to ≥6.2.0 and `vitest` to ≥3.2.0 resolves this. Dev-only; does not affect production bundle. |

### Web UI Security Monitoring

- **Automated:** `npm audit` MUST be run weekly and before each release
- **Manual:** This section MUST be reviewed and updated quarterly
- **New package requests:** Follow `PACKAGE-APPROVAL-PROCESS.md` — no exceptions for "just a small utility"

```bash
# Run web vulnerability check
cd web && npm audit
```

---

## 14. Maintenance Tracking

| Package | Action Required | Deadline | Notes |
|---------|----------------|----------|-------|
| `github.com/labstack/echo/v4` | Evaluate migration to Echo v5 | 2026-12-31 | v4 end-of-support date. Project Phase 1 completion (~June 2026) gives 6-month window for migration planning. |
| `vite` + `vitest` | Upgrade to fix esbuild GHSA-67mh-4wv8-2f99 | 2026-03-15 | Moderate severity, dev-only. Upgrade vite to ≥6.2.0 and vitest to ≥3.2.0. |
| `react` | Evaluate React 19 migration | 2026-09-01 | React 19 stable; plan migration when ecosystem stabilizes. |
