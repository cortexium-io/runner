GO ?= go
# Child fixture commands must use the selected compiler too.
export PATH := $(shell $(GO) env GOROOT)/bin:$(PATH)
PACKAGES ?=
TESTS ?= .

.PHONY: check check-workflows check-full

# Routine checks must name the affected packages; narrow TESTS when appropriate.
check:
	@test -n "$(PACKAGES)" || { echo 'Set PACKAGES to the affected Go packages (and optionally TESTS).'; exit 1; }
	$(GO) test -race -timeout 5m -skip '^TestWorkflow' -run '$(value TESTS)' $(PACKAGES)
	$(GO) vet $(PACKAGES)

# Run once after a completed batch affecting workflow execution.
check-workflows:
	$(GO) test -v -race -timeout 20m -run '^TestWorkflow' ./internal/runner/execution

# Explicit final whole-repository gate; never the default repair-loop command.
check-full:
	$(GO) test -race -timeout 20m ./...
	$(GO) vet ./...
