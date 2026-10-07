SHELL := /bin/sh

export CGO_ENABLED = 0

BIN_DIR    := bin
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS    := -s -w \
	-X github.com/strazahq/straza/internal/version.Version=$(VERSION) \
	-X github.com/strazahq/straza/internal/version.Commit=$(COMMIT)

GOBIN_EXT :=
ifeq ($(OS),Windows_NT)
GOBIN_EXT := .exe
endif

.PHONY: all build test lint vet check lengths security verify-release bench tidy clean size perf perf-reference harness-matrix e2e-matrix docs-gen docs-drift ui ui-test

# Maintainer gates live in Makefile.internal, which the public tree does not
# carry: INTERNAL_GATES is empty without it and make check still runs.
-include Makefile.internal

all: build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/strazad$(GOBIN_EXT) ./cmd/strazad
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/strazactl$(GOBIN_EXT) ./cmd/strazactl
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/straza$(GOBIN_EXT) ./cmd/straza

# The suite runs twice: under the race detector, which needs cgo, and with
# cgo off, the way the release binaries are built. Neither pass sets
# -count=1, so an unchanged package reads its result from the test cache.
# A later run of the cgo-off pass with the same flags reuses that pass's
# results after a green make check. 25m matches .github/workflows/ci.yml, because
# internal/server under -race outgrew Go's default 10-minute package timeout.
TEST_TIMEOUT ?= 25m

test:
	CGO_ENABLED=1 go test -race -timeout $(TEST_TIMEOUT) ./...
	CGO_ENABLED=0 go test -timeout $(TEST_TIMEOUT) ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# File-length ratchet (no Go source file over about 600 lines):
# the count of non-test Go source files over 600 lines may only go DOWN. Lower
# FILE_LENGTH_MAX when a split lands; never raise it. `make check` and CI
# share this one number.
FILE_LENGTH_MAX ?= 7

LENGTHS_FIND := find . -name '*.go' -not -name '*_test.go' -not -path './.git/*' -not -path './build/*' -not -path './.tools/*' -not -path './.claude/*' -print0 | xargs -0 wc -l | awk '$$1>600 && $$2!="total"'

lengths:
	@n=$$($(LENGTHS_FIND) | wc -l | tr -d ' '); \
	echo "file-length ratchet: $$n non-test Go files over 600 lines (max $(FILE_LENGTH_MAX))"; \
	if [ "$$n" -gt "$(FILE_LENGTH_MAX)" ]; then \
		echo "STRAZA gate: file-length ratchet exceeded. Split a file; lower FILE_LENGTH_MAX only after a split landed:" >&2; \
		$(LENGTHS_FIND) | sort -rn >&2; \
		exit 1; \
	fi

# Comment-run ratchet: the count of non-test Go files with a comment run
# longer than 15 lines may only go DOWN. Doc comments state the contract.
# Lower COMMENT_RUN_MAX when a trim lands; never raise it.
COMMENT_RUN_MAX ?= 0

COMMENTS_FIND := find . -name '*.go' -not -name '*_test.go' -not -path './.git/*' -not -path './build/*' -not -path './.tools/*' -not -path './.claude/*' -not -path '*/vendor/*' -not -path '*/dist/*'

comments:
	@n=0; over=""; \
	for f in $$($(COMMENTS_FIND)); do \
		r=$$(awk 'BEGIN{m=0;r=0} /^[[:space:]]*\/\//{r++; if(r>m)m=r; next} {r=0} END{print m}' "$$f"); \
		if [ "$$r" -gt 15 ]; then n=$$((n+1)); over="$$over\n  $$r $$f"; fi; \
	done; \
	echo "comment-run ratchet: $$n non-test Go files with a comment run over 15 lines (max $(COMMENT_RUN_MAX))"; \
	if [ "$$n" -gt "$(COMMENT_RUN_MAX)" ]; then \
		printf 'STRAZA gate: comment-run ratchet exceeded. Trim a comment; lower COMMENT_RUN_MAX only after a trim landed:%b\n' "$$over" >&2; \
		exit 1; \
	fi

# Generated reference pages: the CLI pages come from each binary's command
# tree under the docsgen build tag, the configuration page from the knob
# table and the API page from pkg/api/openapi.yaml. docs-drift refuses a
# tree whose committed pages differ from the generated ones, in the shape of
# the console dist gate.
DOCS_GENERATED := website/content/reference/cli website/content/reference/configuration.md website/content/reference/api.md website/static/.well-known
docs-gen:
	go run -tags docsgen ./cmd/strazad gen-docs website/content/reference/cli/strazad
	go run -tags docsgen ./cmd/strazactl gen-docs website/content/reference/cli/strazactl
	go run -tags docsgen ./cmd/straza gen-docs website/content/reference/cli/straza
	go run ./tools/docsgen config website/content/reference/configuration.md
	go run ./tools/docsgen api website/content/reference/api.md
	go run ./tools/docsgen skills website/static/.well-known

docs-drift: docs-gen
	@if ! git diff --quiet -- $(DOCS_GENERATED) || [ -n "$$(git ls-files --others --exclude-standard -- $(DOCS_GENERATED))" ]; then \
		echo "STRAZA gate: generated reference pages are stale. Run make docs-gen, then git add website/content/reference:" >&2; \
		git status --porcelain -- $(DOCS_GENERATED) >&2; \
		exit 1; \
	fi

# check: everything must be green.
check: vet lint lengths comments $(INTERNAL_GATES) docs-drift plugins-gate test ui ui-test

# --- security: the supply-chain gate ---------------------------------------
# `make security` runs the vulnerability, dependency and secret scans on a
# developer machine, beside the `vulncheck` job in .github/workflows/ci.yml.

# Deliberately NOT wired into `check`: every tool below refreshes an advisory
# database over the network, and a commit gate must never depend on the
# network. Run this before pushing and before a tag.

# Every tool is REQUIRED: a missing one FAILS the target with its install
# line, because skipping quietly would hide a gap. The single exception is
# scorecard, which grades long-lived repo hygiene rather than this commit;
# its exit code is ignored, its output never is. A scorecard run that
# outlasts SCORECARD_TIMEOUT seconds is stopped, and the target says so.
SECURITY_TOOLS := govulncheck osv-scanner gitleaks scorecard
SECURITY_OUT   := build/security
# GITLEAKS_CONFIG is the allowlist gitleaks reads. A checkout may point it at a
# file that extends .gitleaks.toml with entries for its own history.
GITLEAKS_CONFIG ?= .gitleaks.toml
SCORECARD_TIMEOUT ?= 120

security:
	@missing=""; \
	for t in $(SECURITY_TOOLS); do \
	  command -v $$t >/dev/null 2>&1 || missing="$$missing $$t"; \
	done; \
	if [ -n "$$missing" ]; then \
	  echo "make security: REFUSING to run a partial scan. Missing:$$missing" >&2; \
	  echo "" >&2; \
	  echo "  install (user-level, no sudo; all four are Go modules):" >&2; \
	  echo "    go install golang.org/x/vuln/cmd/govulncheck@latest" >&2; \
	  echo "    go install github.com/google/osv-scanner/v2/cmd/osv-scanner@latest" >&2; \
	  echo "    go install github.com/zricethezav/gitleaks/v8@latest" >&2; \
	  echo "    go install github.com/ossf/scorecard/v5@latest" >&2; \
	  echo "  then put \"\$$(go env GOPATH)/bin\" on your PATH." >&2; \
	  exit 1; \
	fi
	@mkdir -p $(SECURITY_OUT)
	@echo "== govulncheck: known vulns in the Go module graph + stdlib (call-graph aware) =="
	@govulncheck ./... > $(SECURITY_OUT)/govulncheck.txt 2>&1; rc=$$?; \
	  cat $(SECURITY_OUT)/govulncheck.txt; \
	  [ $$rc -eq 0 ] || { echo "make security: govulncheck FAILED" >&2; exit $$rc; }
	@echo ""
	@echo "== osv-scanner: every dependency manifest/lockfile in the tree (the non-Go"
	@echo "   surface govulncheck structurally cannot see) =="
	@osv-scanner scan source -r . > $(SECURITY_OUT)/osv-scanner.txt 2>&1; rc=$$?; \
	  cat $(SECURITY_OUT)/osv-scanner.txt; \
	  [ $$rc -eq 0 ] || { echo "make security: osv-scanner FAILED" >&2; exit $$rc; }
	@echo ""
	@echo "== gitleaks: secrets across the WHOLE git history, not just the worktree"
	@echo "   (allowlist + per-entry fakeness proofs: $(GITLEAKS_CONFIG)) =="
	@gitleaks detect --redact --no-banner --config $(GITLEAKS_CONFIG) \
	  --report-format json --report-path $(SECURITY_OUT)/gitleaks.json 2>&1; rc=$$?; \
	  [ $$rc -eq 0 ] || { echo "make security: gitleaks FOUND SECRETS -> $(SECURITY_OUT)/gitleaks.json" >&2; exit $$rc; }
	@echo ""
	@echo "== scorecard: OpenSSF repo hygiene, INFORMATIONAL (never fails this target) =="
	@# Scored against a pristine `git archive HEAD` export, not the working tree:
	@# scorecard grades what is COMMITTED, and a single unreadable local scratch
	@# directory (an overlayfs work dir under test/harness-matrix/.work) otherwise
	@# aborts its file walk and turns every check into "?".
	@rm -rf $(SECURITY_OUT)/tree && mkdir -p $(SECURITY_OUT)/tree
	@git archive --format=tar HEAD | tar -x -C $(SECURITY_OUT)/tree
	@# scorecard asks public registries for image digests, which needs no login,
	@# so it gets a Docker config of its own that names no credential helper. The
	@# helper in an operator's own config can wait forever. The config file is
	@# written and not left out because the registry library otherwise reads the
	@# podman login file on a box that has no Docker config.
	@# The step is also stopped after SCORECARD_TIMEOUT seconds, 120 by default,
	@# which `make security SCORECARD_TIMEOUT=300` changes. Under bash the stop
	@# also ends what scorecard started. Any other shell ends scorecard alone.
	@mkdir -p $(SECURITY_OUT)/docker && echo '{}' > $(SECURITY_OUT)/docker/config.json
	@[ -z "$$BASH_VERSION" ] || set -m; \
	  DOCKER_CONFIG=$(SECURITY_OUT)/docker scorecard --local $(SECURITY_OUT)/tree --show-details \
	    < /dev/null > $(SECURITY_OUT)/scorecard.txt 2>&1 & pid=$$!; set +m; n=0; \
	  stop_scorecard() { kill -KILL -$$pid 2>/dev/null || kill -KILL $$pid 2>/dev/null; }; \
	  trap 'stop_scorecard; exit 130' INT TERM HUP; \
	  while kill -0 $$pid 2>/dev/null && [ $$n -lt $(SCORECARD_TIMEOUT) ]; do sleep 1; n=$$((n+1)); done; \
	  if kill -0 $$pid 2>/dev/null; then stop_scorecard; \
	    echo "make security: scorecard did not finish within $(SCORECARD_TIMEOUT) seconds and was stopped, most likely while waiting on a container registry or the vulnerability database." \
	      "The step is informational, so nothing failed and the push goes on. Run make security again later to get the hygiene table."; \
	  fi; \
	  wait $$pid 2>/dev/null || true
	@sed -n '/^RESULTS/,$$p' $(SECURITY_OUT)/scorecard.txt
	@rm -rf $(SECURITY_OUT)/tree $(SECURITY_OUT)/docker
	@echo ""
	@echo "make security: OK. Reports in $(SECURITY_OUT)/."

# verify-release: reproduce a release locally and prove the published bytes
# are the ones this source tree produces. See tools/verify-release.sh for the
# full contract and for the key-based cosign fallback used when the keyless
# (GitHub OIDC) path is unavailable.
verify-release:
	tools/verify-release.sh

# plugins-gate: the skill bundle under plugins/ stays true to the product.
# tools/pluginsgate refuses a CLI command, verb or flag that no generated
# reference page carries, a docs link with no page, an example document the
# validators reject, a SKILL.md without a compatibility line, and manifests
# whose versions disagree. A change to plugins/ also bumps the version.
# The served copy
# under website/static/.well-known comes from make docs-gen.
plugins-gate:
	@go run ./tools/pluginsgate && echo "plugins gate: plugins/ is current"

bench:
	go test -run '^$$' -bench . -benchmem ./...

# ui: compile the console app (web/ui -> internal/server/console/dist,
# checked in). Needs Node 22 and npm: tools/uibuild refuses loudly without
# them, installs the locked tree with npm ci and checks the gzipped budget
# per entry page. Run after editing web/ui.
ui:
	go run ./tools/uibuild

# ui-test: the app's vitest suites over its source (jsdom, Testing Library).
ui-test:
	cd web/ui && npm test

# size: report the frontend byte budgets, one line per entry page of the
# app (gzipped per entry page vs internal/server/console/distbudget), and
# fail if the contract is violated. Thin reporter over the one distbudget
# gate so there is no second budget implementation to drift.
size:
	go test -count=1 -run 'TestUIBudget$$' -v ./internal/server/console

# perf: run the load harness at laptop scale (report only, no gate).
perf: build
	go run ./test/load -straza $(BIN_DIR)/straza$(GOBIN_EXT) -report perf-report.json

# perf-reference: the headline perf numbers; run on a dedicated reference box
# (100k idle sessions, 5k rps/node, 100k kill-switch clients), enforcing.
perf-reference: build
	go run ./test/load -sessions 100000 -rps 5000 -duration 60s -workers 128 \
		-kill-clients 100000 -kill-revokes 300 -hook-samples 300 \
		-straza $(BIN_DIR)/straza$(GOBIN_EXT) -enforce -report perf-report.json

# demo-m2: the hook-lane demo: enroll, start a session, block `rm -rf` with
# a reason, allow a benign command, and find the block in the audit trail.
# The authoritative, assertion-backed version is the Go test; this target
# runs it, then points at the human script.
demo-m2:
	go test -count=1 -run TestM2Demo ./internal/agentguard/ -v
	@echo "Human-runnable walkthrough: test/demo/demo-m2.sh"

# demo-m3: the gateway demo (a role sees an app's tools through the gateway;
# a user without the role sees nothing; the token never reaches the client).
# Authoritative assertion-backed version is the Go test.
demo-m3:
	go test -count=1 -run TestM3Demo ./internal/server/ -v
	@echo "Human-runnable walkthrough: test/demo/demo-m3.sh"

# demo-m4: the kill-switch demo (SCIM provision -> enroll -> work -> SCIM
# deactivate -> session loses everything < 5s; same policy identical across
# claude-code/codex/gemini). Authoritative assertion-backed version is the
# Go test; the cross-harness half lives in internal/agentguard.
demo-m4:
	go test -count=1 -run TestM4Demo ./internal/server/ -v
	go test -count=1 -run TestCrossHarnessIdenticalDecisions ./internal/agentguard/ -v
	@echo "Human-runnable walkthrough: test/demo/demo-m4.sh"

# demo-m5: the managed-install demo (require-managed gate: user-mode install
# gets no token; install --managed + hash registration verifies; tampered
# hook wiring -> att=none -> no token -> fully blocked).
demo-m5:
	go test -count=1 -run TestM5Demo ./internal/agentguard/ -v
	go test -count=1 -run 'TestCheckinAttestationGate|TestAttestationGateAdminCLIAndGateway' ./internal/server/ -v

# harness-matrix: live-fire the REAL harness CLIs (codex, gemini, claude-code)
# and the python-kit framework matrix (langchain/openai-agents/claude-agent-sdk)
# against the hook wiring the installer writes; key-free, fully isolated.
# HARNESS_MATRIX_LANES selects lanes;
# CODEX_VERSION/GEMINI_VERSION/CLAUDE_VERSION=latest override the versions.env
# pins (the drift tripwire channel).
harness-matrix:
	bash test/harness-matrix/run.sh

# e2e-matrix: the spec-authored journey corpus against binaries built from
# the tree: a real strazad, the real straza hook on every dialect, the
# gateway, the admin and SCIM APIs and the audit chain (test/e2e-matrix/
# README.md). Loopback only, no model, no docker. STRAZA_E2E_SCENARIOS
# selects a comma-separated subset by id. The report lands in
# test/e2e-matrix/.work/report.md.
e2e-matrix:
	STRAZA_E2E_MATRIX=1 go test -count=1 -timeout 25m -run TestJourneys -v ./test/e2e-matrix

tidy:
	go mod tidy

clean:
	rm -rf $(BIN_DIR)
