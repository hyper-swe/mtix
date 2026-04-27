# mtix — AI-native micro issue manager
# Full build system: Go backend + React frontend + embedded SPA

# ─── Version info ───
# Tags: use git tags for release versions (e.g. v0.1.0)
# Default: dev build with commit hash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# ─── Paths ───
BINARY    := mtix
LDFLAGS   := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
GOFLAGS   := -trimpath
COVERFILE := cover.out
WEB_DIR   := web
EMBED_DIR := internal/web/dist

# ─── Go packages (excludes third-party Go code in node_modules) ───
GO_PKGS   := $(shell go list ./... | grep -v node_modules)

# ─── Task tracking ───
TASKS_EXPORT := .mtix/tasks.json
TASKS_DB     := .mtix/data/mtix.db

# ─── Phony targets ───
.PHONY: all build build-go build-web install-web test test-go test-web \
        test-race test-cover test-all lint lint-go lint-web \
        security-scan security-audit bench fuzz e2e proto-gen docs-gen \
        release-artifacts clean verify preflight help \
        setup tasks-export tasks-import tasks-sync \
        generate-plugin-skills agent-kit \
        test-pg-docker test-pg-supabase test-pg-neon test-pg-all \
        cleanup-test-schemas

# ─── Default target ───
all: build

## ─── Build targets ───

## build: Build the complete mtix suite (web + Go binary)
build: build-web build-go

## build-go: Compile Go binary with embedded web assets and version info
build-go:
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY) ./cmd/mtix/

## build-web: Build the React SPA and copy to Go embed directory
build-web: install-web
	cd $(WEB_DIR) && npm run build
	rm -rf $(EMBED_DIR)
	cp -r $(WEB_DIR)/dist $(EMBED_DIR)

## install-web: Install web dependencies if needed
install-web:
	@if [ ! -d $(WEB_DIR)/node_modules ]; then \
		echo "Installing web dependencies..."; \
		cd $(WEB_DIR) && npm ci; \
	fi

## build-checked: Run all tests, then build (use for releases)
build-checked: test-all build

## ─── Task tracking ───
## setup: Build mtix and restore task database from git-tracked export
## Run this after cloning or pulling. The database is a local cache;
## .mtix/tasks.json is the git-tracked source of truth.
setup: build
	@if [ ! -f $(TASKS_DB) ] && [ -f $(TASKS_EXPORT) ]; then \
		echo "Restoring task database from $(TASKS_EXPORT)..."; \
		./$(BINARY) init --prefix MTIX 2>/dev/null || true; \
		./$(BINARY) import $(TASKS_EXPORT) --mode replace; \
		echo "Task database restored. Run './mtix list --status open' to see open tasks."; \
	elif [ ! -f $(TASKS_DB) ] && [ ! -f $(TASKS_EXPORT) ]; then \
		echo "No task export found. Run './mtix init --prefix MTIX' to start fresh."; \
	else \
		echo "Task database exists. Use 'make tasks-sync' to export before committing."; \
	fi

## tasks-export: Export current task state to git-tracked JSON file
## Run before committing to ensure task changes are captured in git.
tasks-export:
	@if [ -f $(TASKS_DB) ]; then \
		./$(BINARY) export > $(TASKS_EXPORT); \
		echo "Exported tasks to $(TASKS_EXPORT)"; \
	else \
		echo "No task database. Run 'make setup' first."; \
	fi

## tasks-import: Rebuild task database from git-tracked export
## Use after pulling changes that include task updates.
tasks-import: build
	@if [ -f $(TASKS_EXPORT) ]; then \
		if [ ! -f $(TASKS_DB) ]; then \
			./$(BINARY) init --prefix MTIX 2>/dev/null || true; \
		fi; \
		./$(BINARY) import $(TASKS_EXPORT) --mode replace; \
		echo "Task database rebuilt from $(TASKS_EXPORT)"; \
	else \
		echo "No task export found at $(TASKS_EXPORT)."; \
	fi

## tasks-sync: Export tasks, stage the export file for commit
tasks-sync: tasks-export
	@git add $(TASKS_EXPORT)
	@echo "Staged $(TASKS_EXPORT) for commit."

## ─── Test targets ───

## test: Run Go tests (no cache)
test: test-go

## test-go: Run all Go tests
test-go:
	go test $(GO_PKGS) -count=1

## test-web: Run all web (Vitest) tests
test-web: install-web
	cd $(WEB_DIR) && npx vitest run

## test-all: Run both Go and web tests
test-all: test-go test-web

## test-race: Run Go tests with race detector
test-race:
	go test $(GO_PKGS) -race -count=1

## test-cover: Run Go tests with coverage report
test-cover:
	go test $(GO_PKGS) -coverprofile=$(COVERFILE) -count=1
	go tool cover -func=$(COVERFILE) | tail -1

## ─── Lint targets ───

## lint: Run all linters (Go + web)
lint: lint-go lint-web

## lint-go: Run golangci-lint
lint-go:
	golangci-lint run

## lint-web: Run ESLint on web source
lint-web: install-web
	cd $(WEB_DIR) && npx eslint . --ext ts,tsx --report-unused-disable-directives --max-warnings 0 2>/dev/null || true

## ─── Security & analysis ───

## security-scan: Run gosec and govulncheck
security-scan:
	gosec $(GO_PKGS)
	govulncheck $(GO_PKGS)

## security-audit: Run full security audit (Go + Web) and update release artifacts
security-audit:
	@echo "=== Go vulnerability scan ==="
	govulncheck $(GO_PKGS)
	@echo ""
	@echo "=== Web vulnerability scan ==="
	cd $(WEB_DIR) && npm audit
	@echo ""
	@echo "=== Go security linter ==="
	golangci-lint run
	@echo ""
	@echo "Security audit passed. Update docs/SECURITY-AUDIT.md with findings."

## bench: Run Go benchmarks
bench:
	go test $(GO_PKGS) -bench=. -benchmem -count=5

## fuzz: Run fuzz tests (30s)
fuzz:
	go test $(GO_PKGS) -fuzz=. -fuzztime=30s

## e2e: Run end-to-end tests
e2e:
	go test ./e2e/... -v -count=1

## ─── E2E Postgres harness (MTIX-14.9) ───
## test-pg-docker: Run Postgres contract suite against ephemeral docker container.
## Requires: docker daemon running locally.
test-pg-docker:
	go test ./e2e/postgres/ -tags=e2e -provider=docker -count=1 -v

## test-pg-supabase: Run Postgres contract suite against Supabase.
## Requires: MTIX_TEST_SUPABASE_DSN env var (use the :5432 direct port for full coverage).
test-pg-supabase:
	@if [ -z "$$MTIX_TEST_SUPABASE_DSN" ]; then \
		echo "MTIX_TEST_SUPABASE_DSN not set; skipping supabase tests."; \
		exit 0; \
	fi
	go test ./e2e/postgres/ -tags=e2e -provider=supabase -count=1 -v

## test-pg-neon: Run Postgres contract suite against Neon serverless.
## Requires: MTIX_TEST_NEON_DSN env var.
test-pg-neon:
	@if [ -z "$$MTIX_TEST_NEON_DSN" ]; then \
		echo "MTIX_TEST_NEON_DSN not set; skipping neon tests."; \
		exit 0; \
	fi
	go test ./e2e/postgres/ -tags=e2e -provider=neon -count=1 -v

## test-pg-all: Run every Postgres provider whose credentials are present.
## Always runs docker (if available); cloud providers run only when DSN env vars are set.
test-pg-all: test-pg-docker
	@$(MAKE) test-pg-supabase
	@$(MAKE) test-pg-neon

## cleanup-test-schemas: Drop orphaned mtix_test_* schemas from a Postgres database.
## Defaults to dry-run. Pass DRY_RUN=false to actually drop.
##   make cleanup-test-schemas DSN="$$MTIX_TEST_SUPABASE_DSN" OLDER_THAN=24h
DRY_RUN     ?= true
OLDER_THAN  ?= 24h
DSN         ?= $$MTIX_CLEANUP_DSN
cleanup-test-schemas:
	go run -tags=cleanup ./tools/cleanup-test-schemas.go \
		-dsn "$(DSN)" \
		-older-than $(OLDER_THAN) \
		-dry-run=$(DRY_RUN)

## ─── Code generation ───

## proto-gen: Generate protobuf Go and Python code
proto-gen:
	@echo "Generating Go protobuf code..."
	mkdir -p internal/api/grpc/pb
	cd proto && buf generate
	@echo "Generating Python protobuf code..."
	mkdir -p sdk/python/mtix/pb
	python3 -m grpc_tools.protoc \
		-Iproto \
		--python_out=sdk/python/mtix/pb \
		--grpc_python_out=sdk/python/mtix/pb \
		proto/mtix/v1/types.proto proto/mtix/v1/mtix.proto
	@echo "Proto generation complete."

## docs-gen: Generate documentation
docs-gen:
	go run ./cmd/mtix/ docs generate

## ─── Cleanup ───

## clean: Remove all build artifacts
clean:
	rm -f $(BINARY) $(COVERFILE)
	rm -rf $(EMBED_DIR)
	rm -rf $(WEB_DIR)/dist

## ─── Verification ───

## verify: Full verification checklist (pre-commit / pre-release)
verify: test-race test-web lint
	go test $(GO_PKGS) -coverprofile=$(COVERFILE) -count=1
	@echo ""
	@echo "=== Coverage report ==="
	@go tool cover -func=$(COVERFILE) | tail -1
	@echo ""
	@echo "=== Web vulnerability scan ==="
	@cd $(WEB_DIR) && npm audit
	@echo ""
	@echo "=== Building ==="
	@$(MAKE) build
	@echo ""
	@echo "=== Version ==="
	@./$(BINARY) version 2>/dev/null || echo "$(VERSION) ($(COMMIT)) built $(DATE)"
	@echo ""
	@echo "All checks passed."

## ─── Release readiness ───

## Coverage threshold for preflight gate (percent)
COVERAGE_MIN := 85

## preflight: Full release readiness evaluation — run before tagging a release
## Inspired by NASA pre-flight checklists. Runs all quality gates and prints
## a summary scorecard. Exits non-zero if any critical check fails.
preflight:
	@echo "╔══════════════════════════════════════════════════════════════╗"
	@echo "║              mtix PRE-FLIGHT CHECKLIST                     ║"
	@echo "╚══════════════════════════════════════════════════════════════╝"
	@echo ""
	@PASS=0; WARN=0; FAIL=0; PROJDIR=$$(pwd); \
	\
	echo "① Linting (golangci-lint)..."; \
	if golangci-lint run > /dev/null 2>&1; then \
		echo "  ✓ PASS: Zero lint issues"; PASS=$$((PASS+1)); \
	else \
		echo "  ✗ FAIL: Lint issues found"; FAIL=$$((FAIL+1)); \
		golangci-lint run 2>&1 | head -10; \
	fi; \
	echo ""; \
	\
	echo "② Go tests (with race detector)..."; \
	if go test $(GO_PKGS) -race -count=1 > /dev/null 2>&1; then \
		echo "  ✓ PASS: All Go tests pass (race-clean)"; PASS=$$((PASS+1)); \
	else \
		echo "  ✗ FAIL: Go tests failed"; FAIL=$$((FAIL+1)); \
	fi; \
	echo ""; \
	\
	echo "③ Web tests (Vitest)..."; \
	if (cd "$$PROJDIR/$(WEB_DIR)" && npx vitest run > /dev/null 2>&1); then \
		echo "  ✓ PASS: All web tests pass"; PASS=$$((PASS+1)); \
	else \
		echo "  ✗ FAIL: Web tests failed"; FAIL=$$((FAIL+1)); \
	fi; \
	echo ""; \
	\
	echo "④ Test coverage..."; \
	go test $(GO_PKGS) -coverprofile=$(COVERFILE) -count=1 > /dev/null 2>&1; \
	COV_LINE=$$(go tool cover -func=$(COVERFILE) 2>/dev/null | tail -1); \
	COV=$$(echo "$$COV_LINE" | grep -oE '[0-9]+\.[0-9]+' | tail -1); \
	COV_INT=$${COV%%.*}; \
	echo "  Overall: $${COV}% (threshold: $(COVERAGE_MIN)%)"; \
	if [ "$$COV_INT" -ge "$(COVERAGE_MIN)" ] 2>/dev/null; then \
		echo "  ✓ PASS: Coverage meets threshold"; PASS=$$((PASS+1)); \
	else \
		echo "  ⚠ WARN: Coverage below threshold"; WARN=$$((WARN+1)); \
	fi; \
	echo ""; \
	\
	echo "⑤ NPM audit..."; \
	AUDIT=$$(cd "$$PROJDIR/$(WEB_DIR)" && npm audit 2>&1); \
	if echo "$$AUDIT" | grep -q "found 0 vulnerabilities"; then \
		echo "  ✓ PASS: Zero npm vulnerabilities"; PASS=$$((PASS+1)); \
	else \
		echo "  ✗ FAIL: npm vulnerabilities found"; FAIL=$$((FAIL+1)); \
		echo "$$AUDIT" | tail -3; \
	fi; \
	echo ""; \
	\
	echo "⑥ Go vulnerability scan..."; \
	if command -v govulncheck > /dev/null 2>&1; then \
		if govulncheck $(GO_PKGS) 2>&1 | grep -q "No vulnerabilities found"; then \
			echo "  ✓ PASS: No Go vulnerabilities"; PASS=$$((PASS+1)); \
		else \
			echo "  ⚠ WARN: govulncheck findings — review output"; WARN=$$((WARN+1)); \
		fi; \
	else \
		echo "  ⚠ WARN: govulncheck not installed (go install golang.org/x/vuln/cmd/govulncheck@latest)"; WARN=$$((WARN+1)); \
	fi; \
	echo ""; \
	\
	echo "⑦ Build..."; \
	if $(MAKE) -C "$$PROJDIR" build > /dev/null 2>&1; then \
		echo "  ✓ PASS: Binary builds successfully"; PASS=$$((PASS+1)); \
	else \
		echo "  ✗ FAIL: Build failed"; FAIL=$$((FAIL+1)); \
	fi; \
	echo ""; \
	\
	echo "⑧ LICENSE file..."; \
	if [ -f LICENSE ] || [ -f LICENSE.md ] || [ -f LICENCE ]; then \
		echo "  ✓ PASS: LICENSE file exists"; PASS=$$((PASS+1)); \
	else \
		echo "  ⚠ WARN: No LICENSE file found"; WARN=$$((WARN+1)); \
	fi; \
	echo ""; \
	\
	echo "⑨ Untracked files..."; \
	UNTRACKED=$$(git ls-files --others --exclude-standard 2>/dev/null | grep -v node_modules | grep -v '.mtix/data' | head -20); \
	if [ -z "$$UNTRACKED" ]; then \
		echo "  ✓ PASS: No untracked files"; PASS=$$((PASS+1)); \
	else \
		UCOUNT=$$(echo "$$UNTRACKED" | wc -l | tr -d ' '); \
		echo "  ⚠ WARN: $$UCOUNT untracked files (may need committing or .gitignore)"; WARN=$$((WARN+1)); \
		echo "$$UNTRACKED" | head -5 | sed 's/^/    /'; \
	fi; \
	echo ""; \
	\
	echo "⑩ Task completion (mtix stats)..."; \
	if [ -f ./$(BINARY) ]; then \
		./$(BINARY) stats 2>/dev/null | head -8 | sed 's/^/  /'; \
		echo "  (info only — not a gate)"; \
	else \
		echo "  (binary not available — skipped)"; \
	fi; \
	echo ""; \
	\
	echo "═══════════════════════════════════════════════════════════════"; \
	echo "  SCORECARD:  ✓ $$PASS passed   ⚠ $$WARN warnings   ✗ $$FAIL failed"; \
	echo "  VERSION:    $(VERSION)"; \
	echo "═══════════════════════════════════════════════════════════════"; \
	echo ""; \
	if [ $$FAIL -gt 0 ]; then \
		echo "❌ PREFLIGHT FAILED — $$FAIL critical check(s) must be resolved."; \
		exit 1; \
	elif [ $$WARN -gt 0 ]; then \
		echo "⚠  PREFLIGHT PASSED WITH WARNINGS — review before release."; \
	else \
		echo "✅ PREFLIGHT PASSED — all systems go."; \
	fi

## ─── Release artifacts ───

## release-artifacts: Generate coverage and security reports for release notes
release-artifacts:
	@echo "=== Generating release artifacts ==="
	@echo ""
	@echo "--- Go coverage report ---"
	@go test $(GO_PKGS) -coverprofile=$(COVERFILE) -count=1 > /dev/null 2>&1
	@go tool cover -func=$(COVERFILE) | tail -1
	@echo ""
	@echo "--- Web coverage report ---"
	@cd $(WEB_DIR) && npx vitest run --coverage 2>&1 | tail -5
	@echo ""
	@echo "--- Go vulnerability scan ---"
	@govulncheck $(GO_PKGS) 2>&1 || true
	@echo ""
	@echo "--- Web vulnerability scan ---"
	@cd $(WEB_DIR) && npm audit 2>&1 || true
	@echo ""
	@echo "Release artifacts in docs/:"
	@echo "  docs/SECURITY-AUDIT.md   — Security audit report"
	@echo "  docs/COVERAGE-REPORT.md  — Code coverage report"
	@echo ""
	@echo "Update these files with current results before tagging the release."

## ─── Release helpers ───

## release-patch: Tag a patch release (v0.0.X)
release-patch: release-artifacts
	@CURRENT=$$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0"); \
	MAJOR=$$(echo $$CURRENT | cut -d. -f1); \
	MINOR=$$(echo $$CURRENT | cut -d. -f2); \
	PATCH=$$(echo $$CURRENT | cut -d. -f3); \
	NEW="$$MAJOR.$$MINOR.$$((PATCH + 1))"; \
	echo "Tagging $$NEW (was $$CURRENT)"; \
	git tag -a "$$NEW" -m "Release $$NEW"

## release-minor: Tag a minor release (v0.X.0)
release-minor: release-artifacts
	@CURRENT=$$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0"); \
	MAJOR=$$(echo $$CURRENT | cut -d. -f1); \
	MINOR=$$(echo $$CURRENT | cut -d. -f2); \
	NEW="$$MAJOR.$$((MINOR + 1)).0"; \
	echo "Tagging $$NEW (was $$CURRENT)"; \
	git tag -a "$$NEW" -m "Release $$NEW"

## ─── Plugin and agent kit ───

## generate-plugin-skills: Render skill templates to .claude-plugin/skills/
generate-plugin-skills: build-go
	@echo "=== Generating plugin skill files ==="
	@./$(BINARY) docs generate --force > /dev/null 2>&1 || true
	@mkdir -p .claude-plugin/skills/references
	@if [ -d .mtix/docs ]; then \
		for f in .mtix/docs/*.md; do \
			cp "$$f" .claude-plugin/skills/ 2>/dev/null || true; \
		done; \
	fi
	@echo "Plugin skills generated in .claude-plugin/skills/"

## agent-kit: Build the LLM agent kit tarball
agent-kit: generate-plugin-skills
	@echo "=== Building agent kit ==="
	@VERSION=$$(echo $(VERSION) | sed 's/^v//'); \
	if [ -x scripts/build-agent-kit.sh ]; then \
		bash scripts/build-agent-kit.sh "$$VERSION"; \
	else \
		echo "scripts/build-agent-kit.sh not found — create it first (MTIX-1.4.2)"; \
		exit 1; \
	fi

## help: Show this help
help:
	@echo "mtix build system"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Build:"
	@echo "  build          Build complete suite (web + Go)"
	@echo "  build-go       Build Go binary only"
	@echo "  build-web      Build web SPA only"
	@echo "  build-checked  Test everything, then build"
	@echo ""
	@echo "Test:"
	@echo "  test           Run Go tests"
	@echo "  test-web       Run web tests (Vitest)"
	@echo "  test-all       Run all tests (Go + web)"
	@echo "  test-race      Run Go tests with race detector"
	@echo "  test-cover     Run Go tests with coverage"
	@echo ""
	@echo "Quality:"
	@echo "  lint              Run all linters"
	@echo "  verify            Full pre-commit verification"
	@echo "  preflight         Full release readiness evaluation (pre-flight checklist)"
	@echo "  security-scan     Run security scanners (gosec + govulncheck)"
	@echo "  security-audit    Full security audit (Go + Web)"
	@echo ""
	@echo "Release:"
	@echo "  release-artifacts Generate coverage + security reports"
	@echo "  release-patch          Tag patch version bump (runs artifacts first)"
	@echo "  release-minor          Tag minor version bump (runs artifacts first)"
	@echo "  generate-plugin-skills Render skill files to .claude-plugin/skills/"
	@echo "  agent-kit              Build LLM agent kit tarball"
	@echo ""
	@echo "Other:"
	@echo "  clean          Remove build artifacts"
	@echo "  proto-gen      Generate protobuf code"
	@echo "  help           Show this help"
	@echo ""
	@echo "Current version: $(VERSION)"
