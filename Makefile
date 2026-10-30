.DEFAULT_GOAL := help
VERSION ?= $(shell jq -r .version .claude-plugin/plugin.json)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: demo help build test cover lint run plugin-validate plugin-install plugin-uninstall codex-install snapshot

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

build: ## build ./bin/budgeteer
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/budgeteer ./cmd/budgeteer

test: ## run tests with the race detector
	go test ./... -race -count=1

cover: ## coverage summary
	go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | tail -1

lint: ## gofmt + vet + shellcheck
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	@command -v shellcheck >/dev/null && shellcheck bin/budgeteer-run || echo "shellcheck not installed; skipped"

run: build ## run the report on your own data
	./bin/budgeteer

plugin-validate: ## validate manifests the way the plugin directory does
	claude plugin validate . --profile directory

plugin-install: ## install this checkout into your Claude Code as a local marketplace
	claude plugin marketplace add "$(CURDIR)"
	claude plugin install budgeteer@budgeteer

plugin-uninstall: ## remove it again
	-claude plugin uninstall budgeteer@budgeteer
	-claude plugin marketplace remove budgeteer

codex-install: ## install the /budgeteer prompt for Codex
	mkdir -p $(HOME)/.codex/prompts
	cp integrations/codex/budgeteer.md $(HOME)/.codex/prompts/budgeteer.md

snapshot: ## local cross-platform release build (needs goreleaser)
	goreleaser release --snapshot --clean --skip=publish

demo: ## regenerate docs/demo.gif from synthetic logs (needs python3 + Pillow, macOS Menlo font)
	python3 scripts/demo/gen.py $(CURDIR)/.demo-home
	go build -o .demo-home/budgeteer ./cmd/budgeteer
	python3 scripts/demo/render.py .demo-home/budgeteer .demo-home docs/demo.gif
	rm -rf .demo-home
