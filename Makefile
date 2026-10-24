BIN := bin/acornfall
DATA ?= $(HOME)/.acornfall

# Listen on every interface so another device can open the console; override
# HOST=127.0.0.1 for a loopback-only run.
HOST ?= 0.0.0.0
PORT ?= 7777
URL ?= http://127.0.0.1:7777

.PHONY: help deps build typecheck lint modernize analyze tools commits check test cover test-sandbox test-bwrap sandbox-preflight ui ui-build run dev fmt clean release release-branch

# The linters, installed into the Go and bun toolchains rather than vendored. `make lint` runs
# whichever are present and names the ones that are not, so a missing tool is a message and
# not a silent pass.
GOBIN := $(shell go env GOPATH)/bin
STATICCHECK := $(GOBIN)/staticcheck
# modernize is the analyzer gopls serves to editors, with a command published as a vet tool.
# x/tools v0.52.0 moved it out of gopls and into go/analysis/passes; gopls v0.24.0 no longer
# carries the old path, which is what stopped the daily workflow installing it.
MODERNIZE := $(GOBIN)/modernize
# Complexity, duplication, dead assignments and error shape, named rule by rule in
# gateway/.golangci.yml. It also runs staticcheck, so it replaced the separate call rather than
# adding to it.
GOLANGCI := $(GOBIN)/golangci-lint
# Unreachable code, which `unused` cannot see across function boundaries.
DEADCODE := $(GOBIN)/deadcode
# The two that need a vulnerability database, so they cannot live in `lint`.
# Mutation testing: coverage says a line ran, this says whether a test would notice.
GREMLINS := $(GOBIN)/gremlins
GOVULNCHECK := $(GOBIN)/govulncheck
GOSEC := $(GOBIN)/gosec
# The language version the gateway is written against, from its own go.mod. The linters must
# be built with at least it: a staticcheck built by an older Go refuses to read newer sources
# and reports the toolchain's standard library as broken. `go install pkg@version` ignores
# this module, so the version is forced. GOTOOLCHAIN wants a full version, and a `go 1.27`
# directive names go1.27.0.
GO_VERSION := $(shell awk '/^go /{print $$2; exit}' gateway/go.mod)
GO_TOOLCHAIN := go$(if $(filter 2,$(words $(subst ., ,$(GO_VERSION)))),$(GO_VERSION).0,$(GO_VERSION))

help:
	@grep -E '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

deps: ## install worker, interface, and tool dependencies
	cd worker && bun install
	cd ui && bun install
	cd tools && bun install

build: ui-build ## build the gateway binary, with the interface embedded
	# CGO off, so the binary is static: it runs as a sandbox's first process, and an image may
	# have no libc at all (a musl image, or none). The release builds it this way too.
	cd gateway && CGO_ENABLED=0 go build -o ../$(BIN) ./cmd/acornfall
	# The release also ships macOS binaries, so a file that only builds on Linux is a broken
	# release rather than a broken test: this is the cheapest place to notice, and the v0.13.1
	# release failed on exactly that.
	cd gateway && GOOS=darwin GOARCH=arm64 go build ./...

ui-build: ## build the interface into the embed directory the gateway serves
	cd ui && bun run build
	@touch gateway/web/dist/.gitkeep

# Idempotent on purpose. `go install pkg@latest` recompiles whether or not the binary is already
# there, which is nothing on a workstation and four minutes on a fresh CI runner — five linters,
# from source. The install is skipped when the binary exists, so a cached ~/go/bin is worth
# something and the pipeline stays inside two minutes. `make tools TOOLS_FORCE=1` installs again,
# and the daily modernize workflow asks for the current release on its own.
define install_if_missing
	@[ -n "$(TOOLS_FORCE)" ] || [ -x "$(1)" ] || { echo "installing $(2)"; GOTOOLCHAIN=$(GO_TOOLCHAIN) go install $(2)@latest; }
endef

tools: ## install the linters used by `make lint` and `make analyze`, if they are missing
	$(call install_if_missing,$(STATICCHECK),honnef.co/go/tools/cmd/staticcheck)
	$(call install_if_missing,$(GOLANGCI),github.com/golangci/golangci-lint/v2/cmd/golangci-lint)
	$(call install_if_missing,$(DEADCODE),golang.org/x/tools/cmd/deadcode)
	$(call install_if_missing,$(GREMLINS),github.com/go-gremlins/gremlins/cmd/gremlins)
	$(call install_if_missing,$(GOVULNCHECK),golang.org/x/vuln/cmd/govulncheck)
	$(call install_if_missing,$(GOSEC),github.com/securego/gosec/v2/cmd/gosec)
	cd worker && bun install

typecheck: ## the compiler's opinion, without running anything
	cd worker && bun run typecheck
	cd ui && bun run typecheck

lint: ui-build typecheck ## the static rules; fails on the first one broken
	@out=$$(gofmt -l gateway); if [ -n "$$out" ]; then echo "$$out"; echo "gofmt: not formatted"; exit 1; fi; echo "gofmt: clean"
	cd gateway && go vet ./...
	# The layer boundary from ADR 0002, as a test. It belongs in lint because the cost is a parser
	# and the failure is a package that learned something the layer table says it must not know.
	cd gateway && go test -count=1 ./internal/arch
	# The release ships four binaries, so a file that only compiles for Linux is a broken release
	# rather than a broken test. `make lint` is what CI runs on every push; the v0.13.1 and v0.14.2
	# releases both failed on exactly this, after the suite was green.
	cd gateway && for pair in darwin/arm64 darwin/amd64 linux/arm64; do \
		GOOS=$${pair%/*} GOARCH=$${pair#*/} go build ./... || exit 1; \
	done
	@if [ -x "$(STATICCHECK)" ]; then cd gateway && $(STATICCHECK) ./... && echo "staticcheck: clean"; \
	else echo "staticcheck: not installed (make tools)"; fi
	bun scripts/check-house-rules.mjs
	cd worker && bun run format:check
	cd worker && bun run lint
	# The interface's rules no compiler owns: token-backed classes only, no arbitrary values,
	# and no class on a screen.
	cd ui && bun run lint
	cd ui && bun run check:system
	cd ui && bun run format:check

cover: ## coverage across both processes, merged; FLOOR=0 reports without failing
	@# Two halves, one number. `go test -cover` sees only the unit half: the feature suite starts
	@# the gateway as its own process, so the binary is built for coverage, writes to GOCOVERDIR,
	@# and its counters are joined with the unit profile before anything is judged. Without this
	@# the end-to-end suite counted for nothing, which is not a claim about anyone's diligence.
	rm -rf $(COVER_DIR) && mkdir -p $(COVER_DIR)/e2e
	cd gateway && go test -short -count=1 -parallel $(TEST_PARALLEL) \
		-covermode=atomic -coverpkg=./... -coverprofile=$(COVER_DIR)/unit.cover \
		$$(go list ./... | grep -v /internal/e2e)
	cd gateway && ACORNFALL_NO_SANDBOX=1 ACORNFALL_COVER_DIR=$(COVER_DIR)/e2e \
		go test -short -count=1 -parallel $(TEST_PARALLEL) ./internal/e2e
	cd gateway && go tool covdata textfmt -i=$(COVER_DIR)/e2e -o=$(COVER_DIR)/e2e.cover
	bun scripts/coverage.mjs merge $(COVER_DIR)/merged.cover $(COVER_DIR)/unit.cover $(COVER_DIR)/e2e.cover
	bun scripts/coverage.mjs report $(COVER_DIR)/merged.cover --floor $(FLOOR) \
		--floors .coveragefloors --ignore .coverageignore --root $(CURDIR)

# The complexity and maintainability half of the bar (#102): a report, not a gate. A gate that is
# red from the day it lands is one nobody runs, so this prints the backlog and leaves the exit
# code alone; it joins `make lint` when the queue is empty.
analyze: ## complexity, duplication, dead code, error shape: the backlog as a report
	@if [ -x "$(GOLANGCI)" ]; then cd gateway && $(GOLANGCI) run ./... || true; \
	else echo "golangci-lint: not installed (make tools)"; fi
	@# A symbol is dead only if no platform reaches it: run per GOOS and keep the intersection.
	@# `nestedFromApple` is unreachable on linux and the only caller on darwin, which is exactly
	@# the false positive a single-platform run would report as a deletion.
	@# The feature suite is a second root: a package made only of tests is invisible to a run that
	# starts from the commands, and `sandbox.EngineVersion` is used there and nowhere else.
	@if [ -x "$(DEADCODE)" ]; then cd gateway && tmp=$$(mktemp -d) && \
		for pair in linux/amd64 darwin/arm64; do \
			GOOS=$${pair%/*} GOARCH=$${pair#*/} $(DEADCODE) -test ./cmd/acornfall ./internal/e2e | sort > $$tmp/$${pair%/*}; \
		done; \
		printf '\ndead code, unreachable on both linux and darwin:\n'; \
		comm -12 $$tmp/linux $$tmp/darwin | sed 's/^/  /'; \
		rm -rf $$tmp; \
	else echo "deadcode: not installed (make tools)"; fi

# Mutation testing for one package. Coverage says a line ran; this says whether any assertion would
# have failed if the line were wrong — which is the difference between a test and a call.
#
# Two things to know before the flags make sense. `gremlins` mutates only what the tests cover, so
# the number is a property of this package's own tests, not of the end-to-end suite. And the timeout
# is a coefficient of the baseline test run: here that baseline is milliseconds, so a small
# coefficient means every mutant times out before the test binary compiles. 500 is what makes
# compilation fit.
#
# It is a report with a floor, not a gate on every push: a survivor is a test to write, and the
# floor in .mutationfloors is where a package is today, moving only up.
PKG ?= ./internal/control
MUTATIONS_WORKERS ?= 8
MUTATION_TIMEOUT ?= 500

mutations: ## mutation testing for one package (PKG=./internal/control), against its floors
	@pkg=$${PKG#./}; \
	efficacy=$$(awk -v p="$$pkg" '$$1==p {print $$2}' .mutationfloors); \
	killed=$$(awk -v p="$$pkg" '$$1==p {print $$3}' .mutationfloors); \
	if [ ! -x "$(GREMLINS)" ]; then echo "gremlins: not installed (make tools)"; exit 0; fi; \
	out=$$(mktemp); \
	(cd gateway && $(GREMLINS) unleash --workers $(MUTATIONS_WORKERS) --timeout-coefficient $(MUTATION_TIMEOUT) \
		$${efficacy:+--threshold-efficacy $$efficacy} $(PKG)) > $$out 2>&1; \
	status=$$?; cat $$out; \
	got=$$(sed -n 's/^Killed: \([0-9]*\).*/\1/p' $$out); rm -f $$out; \
	if [ -n "$$killed" ] && [ -n "$$got" ] && [ "$$got" -lt "$$killed" ]; then \
		echo "mutations: $$got mutants killed, the floor is $$killed — an assertion was lost"; exit 1; \
	fi; \
	exit $$status

# The checks that need a network for their database, so they cannot be in `lint`. The same two run
# weekly in .github/workflows/security.yml and gate a release tag; here they are reproducible
# without a runner. gitleaks is absent on purpose: it scans git history, which is the workflow's
# job — the diff on a laptop is not the history a clone carries.
security: ## known vulnerabilities, and the security rules staticcheck does not carry
	@if [ -x "$(GOVULNCHECK)" ]; then cd gateway && $(GOVULNCHECK) ./...; \
	else echo "govulncheck: not installed (make tools)"; fi
	@if [ -x "$(GOSEC)" ]; then cd gateway && $(GOSEC) -quiet ./... || true; \
	else echo "gosec: not installed (make tools)"; fi

# Not part of `make test` or `make lint`, on purpose: this is the one Go check measured in seconds
# rather than milliseconds, and what it finds is drift rather than a bug — a newer spelling of a
# loop, not a wrong answer. It is a prompt, not a gate, and a daily workflow prompts with it.
modernize: ## the idiom analyzer; a daily workflow runs it, `make lint` does not
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go install golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest
	cd gateway && go vet -vettool=$(MODERNIZE) ./... && echo "modernize: clean"

commits: ## check commit messages (RANGE=origin/main..HEAD by default)
	bun scripts/check-commits.mjs $(RANGE)

# The engine release both suites run, read from the Go source the pin lives in (the house-rules
# check keeps the worker's copy equal to it).
ENGINE_VERSION := $(shell awk -F'"' '/const Version = /{print $$2; exit}' gateway/internal/sandbox/microsandbox.go)

# The slow tier's image cache, shared across runs and across both the Go and worker suites so
# an image is pulled once per machine. Nothing may delete it: a fresh state directory per run
# is how a suite meets a registry's anonymous rate limit. /tmp keeps it short enough for the
# engine's runtime sockets (docs/adr/0012-sandbox.md).
#
# Keyed by the engine version, because the store carries its own schema and migrates forward:
# a run on a newer engine leaves a catalog an older one refuses to read ("database schema is
# newer than this msb binary"), which is a poisoned cache that never heals. The 0.7.7 incident
# in scripts/check-house-rules.mjs is the same failure from the other side.
SANDBOX_STATE ?= /tmp/acornfall-e2e-sandboxes-$(ENGINE_VERSION)

# Every `go test` here runs with -shuffle=on: a suite that only passes in declaration order hides
# that it shares something, and the seed is printed on failure so a shuffled run is reproducible.
#
# How many end-to-end tests the fast tier runs at once: one per core, because each test is a
# gateway and a worker under -race, and six of those on a two-core CI runner starve the
# conversations they are waiting for. Override with TEST_PARALLEL.
TEST_PARALLEL ?= $(shell getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)

# How many workspace sandboxes the slow tier runs at once. Six is one per core on the machine
# this was measured on: the 17 sandbox tests took 125s at 2, 85s at 4, 72s at 6, and 76s at 8
# (contention). Override per machine; each sandbox asks for 2 cpus and 2G. The tier is not
# "serial-ish" for a reason any more — the per-test cleanup made parallel VMs safe.
SANDBOX_PARALLEL ?= 6

# Where `make cover` writes the two halves and the joined profile. Kept out of the tree by
# .gitignore: a coverage profile is a run's artefact, not a source.
COVER_DIR ?= $(CURDIR)/.cover

# The floor `make cover` enforces, per package and across both processes. `FLOOR=0` makes it a
# pure report, which is what a checkout that has not been brought to the bar yet wants.
FLOOR ?= 95

check: ui-build typecheck ## fast: the compiler, the bundle, and the unit tests, no processes
	cd worker && bun test
	cd ui && bun test
	# -count=1 because the suite's inputs include worker/src and the built interface, which
	# Go's test cache cannot see: a cached pass would be a pass for code that has changed.
	# The end-to-end suite is excluded: it starts a gateway and a worker per test, and that is
	# its own tier (`make test`). A gate that takes a minute is a gate nobody runs.
	cd gateway && go test -short -count=1 -shuffle=on $$(go list ./... | grep -v /internal/e2e)

test: lint ## the default tier: units and the feature suite, no hypervisor needed
	cd worker && bun test
	# The interface's unit tests are milliseconds and its bundle has to build, or CI's word
	# about the interface means nothing.
	cd ui && bun test
	$(MAKE) ui-build
	# ACORNFALL_NO_SANDBOX forces the refusal path instead of auto-detecting a hypervisor, so
	# the same command behaves the same on a machine with /dev/kvm and in CI. -short skips the
	# tests whose assertions need a sandbox; the rest of the feature suite runs in parallel.
	# -count=1 because the suite's inputs include worker/src and the built interface, which
	# Go's test cache cannot see: a cached pass would be a pass for code that has changed.
	cd gateway && ACORNFALL_NO_SANDBOX=1 go test -short -race -count=1 -shuffle=on -parallel $(TEST_PARALLEL) ./...

test-bwrap: ## the bubblewrap engine end to end: it fetches an image, so it is not part of `make test`	# A bwrap workspace gets its own copy of its image and carries egress with pasta; both are
	# why this is its own tier, the same way the microVM tier needs /dev/kvm. Needs bubblewrap,
	# passt installed: the namespace's address and routes are set over netlink by the gateway
	# itself, so there is nothing else to install. The client and driver tests run in the fast
	# tier already.
	#
	# ACORNFALL_BWRAP_ONLY makes a machine without them fail instead of skip: this target is the
	# one place that says the tier ran, so a skip here would be a green run that tested nothing.
	cd worker && env -u ACORNFALL_NO_SANDBOX ACORNFALL_BWRAP_ONLY=1 bun test test/bwrap.test.ts test/env-conformance.test.ts
	cd gateway && env -u ACORNFALL_NO_SANDBOX ACORNFALL_BWRAP_ONLY=1 ACORNFALL_IMAGE_TEST=1 go test -count=1 -shuffle=on ./internal/e2e/ -run 'Bwrap|TwoEngines|KeepsTheEngine'

test-sandbox: sandbox-preflight ## the engine boundary: live driver, SDK client, and its e2e tests
	# env -u so a shell that exported the fast tier's switch cannot silently turn this into a
	# suite of skips. -race stays here: the sandbox's start, attach, and mount path is only
	# taken in this tier, and the four tests plus the live driver are ~35s instrumented.
	cd worker && env -u ACORNFALL_NO_SANDBOX ACORNFALL_SANDBOX_INTEGRATION=1 MSB_HOME=$(SANDBOX_STATE) bun test test/sandbox.integration.test.ts
	cd gateway && env -u ACORNFALL_NO_SANDBOX ACORNFALL_SANDBOX_ONLY=1 ACORNFALL_LIVE_SANDBOX=1 ACORNFALL_SANDBOX_STATE=$(SANDBOX_STATE) go test -race -count=1 -shuffle=on -parallel $(SANDBOX_PARALLEL) ./...

sandbox-preflight: ## fail, not skip, when there is no hypervisor for the microVM engine
	@if [ "$$(uname -s)" = "Linux" ] && [ ! -r /dev/kvm ]; then \
		echo "the sandbox tiers need a hypervisor and /dev/kvm is not readable; see docs/adr/0012-sandbox.md"; exit 1; \
	fi
	@if [ "$$(uname -s)" = "Darwin" ] && [ "$$(sysctl -n kern.hv_support 2>/dev/null)" != "1" ]; then \
		echo "the sandbox tiers need a hypervisor and this Mac reports none; see docs/adr/0012-sandbox.md"; exit 1; \
	fi

run: build ## run against the current directory
	./$(BIN) start --host $(HOST) --port $(PORT)

dev: build ## run with the scripted dev provider (no api key needed)
	cd worker && ACORNFALL_DEV_PROVIDER=1 ACORNFALL_MODEL=faux/faux-1 ACORNFALL_WORKER=$(CURDIR)/worker ../$(BIN) start --host $(HOST) --port $(PORT) --data-dir $(DATA)

ui: ## check the interface in a real browser (needs a running gateway)
	cd tools && bun run check-ui.mjs $(URL)

ui-dev: ## serve the interface from source with vite, proxying /api to the gateway
	cd ui && VITE_GATEWAY=$(URL) bun run dev

fmt: ## format Go sources
	cd gateway && gofmt -w .

# usage: make release V=v0.9.5 [NOTES="..."]   # v0.9.5-rc.1 for a pre-release
# Tags origin/main itself, never a local ref: the two stale-tag incidents
# both came from tagging before fetching. Fails clean rather than tagging
# the wrong commit, and proves the tag before pushing it. A hyphen suffix
# marks the GitHub release as a pre-release, so the picker can hide it.
#
# A GA tag belongs on main: the update check follows the newest GA, and a
# branch build marked GA would become every install's default. A branch
# that is not merged yet uses release-branch below, whose tag must carry a
# suffix (vX.Y.Z-beta.N); GitHub then marks it a pre-release.
release: ## tag origin/main as V and push the tag (NOTES=release notes)
	@test -n "$(V)" || (echo "usage: make release V=vX.Y.Z[-suffix] [NOTES=...]"; exit 2)
	@echo "$(V)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$$' || (echo "V is vX.Y.Z or vX.Y.Z-rc.N, got $(V)"; exit 2)
	@test -z "$$(git status --porcelain)" || (echo "tree not clean"; exit 2)
	git fetch origin main 'refs/tags/$(V):refs/tags/$(V)-remote' 2>/dev/null || true
	@if git rev-parse --verify --quiet 'refs/tags/$(V)-remote' >/dev/null; then echo "$(V) already on origin"; exit 2; fi
	git fetch origin main
	git tag -a $(V) origin/main -m "$${NOTES:-Release $(V).}"
	@test "$$(git rev-parse '$(V)^{commit}')" = "$$(git rev-parse origin/main)" || (echo "tag missed main"; exit 2)
	git push origin $(V)

# The branch counterpart of release. It refuses a tag without a hyphen
# suffix, because that is the only thing that keeps a branch build out of
# the newest-GA marker. BRANCH defaults to the checked-out branch.
BRANCH ?= $(shell git branch --show-current)
release-branch: ## tag origin/<BRANCH> as V (V must carry a -suffix; NOTES=release notes)
	@test -n "$(V)" || (echo "usage: make release-branch V=vX.Y.Z-suffix [BRANCH=name] [NOTES=...]"; exit 2)
	@echo "$(V)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.]+$$' || (echo "a branch release needs a hyphen suffix, e.g. v0.11.0-beta.1"; exit 2)
	@test -n "$(BRANCH)" || (echo "name a branch"; exit 2)
	@test -z "$$(git status --porcelain)" || (echo "tree not clean"; exit 2)
	git fetch origin $(BRANCH) 'refs/tags/$(V):refs/tags/$(V)-remote' 2>/dev/null || true
	@if git rev-parse --verify --quiet 'refs/tags/$(V)-remote' >/dev/null; then echo "$(V) already on origin"; exit 2; fi
	git fetch origin $(BRANCH)
	git tag -a $(V) origin/$(BRANCH) -m "$${NOTES:-Release $(V).}"
	@test "$$(git rev-parse '$(V)^{commit}')" = "$$(git rev-parse origin/$(BRANCH))" || (echo "tag missed origin/$(BRANCH)"; exit 2)
	git push origin $(V)

clean:
	rm -rf bin
	find gateway/web/dist -mindepth 1 ! -name .gitkeep -delete 2>/dev/null || true
