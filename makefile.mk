VERSION ?= 1.2.0
COMPOSE ?= scripts/proximity.sh
BUILD ?= ../proximity-build

ROOT := $(abspath $(BUILD))
SOURCE := $(ROOT)/source
BINARY ?= $(ROOT)/bin/proximity
COVERAGE := $(ROOT)/coverage.out

export GOCACHE := $(ROOT)/cache/go-build
export GOMODCACHE := $(ROOT)/cache/go-mod
export GOTMPDIR := $(ROOT)/tmp
export TMPDIR := $(ROOT)/tmp
export npm_config_cache := $(ROOT)/cache/npm
export npm_config_update_notifier := false

SYNC = rsync -a --delete --exclude /.git --exclude /bin --exclude /docs \
	--exclude /web/node_modules --exclude /web/dist ./ $(SOURCE)/

.PHONY: all sync deps web build vet test cover coverage-docs dev image up down restart logs screenshots screenshot clean
.PHONY: stand-security stand-tunnel stand-reload stand-config stand-capture stand-dns stand-logs

all: web build

sync:
	@command -v rsync >/dev/null || { echo "make: rsync is required" >&2; exit 1; }
	@mkdir -p $(SOURCE)/web/dist $(ROOT)/bin $(ROOT)/tmp
	@$(SYNC)
	@touch $(SOURCE)/web/dist/.gitkeep

deps: sync
	@cd $(SOURCE)/web && if [ ! node_modules/.package-lock.json -nt package-lock.json ]; then \
		npm ci --no-audit --no-fund; fi

web: deps
	cd $(SOURCE)/web && npm run build

build: sync
	cd $(SOURCE) && CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/proximity

vet: sync
	gofmt -l cmd internal web/embed.go
	cd $(SOURCE) && go vet ./...

test: sync
	cd $(SOURCE) && go test ./...

cover: sync
	cd $(SOURCE) && go test -coverprofile=$(COVERAGE) ./... && go tool cover -func=$(COVERAGE) | tail -1

# Runs the tests and writes the numbers of tests and coverage into README.md and
# docs/development.md, see scripts/coverage.sh.
coverage-docs: sync
	@cd $(SOURCE) && { go test -coverprofile=$(COVERAGE) ./... > $(ROOT)/tmp/coverage.txt || \
		{ cat $(ROOT)/tmp/coverage.txt; exit 1; }; }
	@cd $(SOURCE) && go tool cover -func=$(COVERAGE) | tail -1 >> $(ROOT)/tmp/coverage.txt
	bash scripts/coverage.sh $(ROOT)/tmp/coverage.txt

dev: deps
	@trap 'kill $$watcher 2>/dev/null' EXIT INT TERM; \
		(while sleep 1; do $(SYNC) >/dev/null; done) & watcher=$$!; \
		cd $(SOURCE)/web && npx vite

image:
	PROXIMITY_VERSION=$(VERSION) $(COMPOSE) build

up:
	PROXIMITY_VERSION=$(VERSION) $(COMPOSE) up -d --build

down:
	$(COMPOSE) down

restart:
	$(COMPOSE) restart

logs:
	$(COMPOSE) logs -f --tail 200

screenshots:
	@mkdir -p $(ROOT)/tmp
	SCREENSHOT_TOOLS=$(ROOT)/screenshots bash scripts/screenshots.sh $(ONLY)

# make screenshot <picture> [picture ...]: the words after the goal are picture names.
ifeq (screenshot,$(firstword $(MAKECMDGOALS)))
PICTURES := $(wordlist 2,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))
.PHONY: $(PICTURES)
$(eval $(PICTURES):;@:)
endif

screenshot:
	@test -n "$(PICTURES)" || { echo "usage: make screenshot <picture> [picture ...]" >&2; exit 1; }
	@mkdir -p $(ROOT)/tmp
	SCREENSHOT_TOOLS=$(ROOT)/screenshots bash scripts/screenshots.sh $(PICTURES)

# Ready stands for end-to-end checks, see scripts/stands and AGENTS.md.
stand-security:
	@mkdir -p $(ROOT)/tmp
	bash scripts/stands/pair/security.sh

stand-tunnel:
	@mkdir -p $(ROOT)/tmp
	bash scripts/stands/pair/tunnel.sh

stand-reload:
	@mkdir -p $(ROOT)/tmp
	SCREENSHOT_TOOLS=$(ROOT)/screenshots bash scripts/stands/reload/check.sh

stand-config:
	@mkdir -p $(ROOT)/tmp
	bash scripts/stands/broken-config/check.sh

stand-capture:
	@mkdir -p $(ROOT)/tmp
	bash scripts/stands/capture/check.sh

stand-dns:
	@mkdir -p $(ROOT)/tmp
	bash scripts/stands/dns/check.sh

stand-logs:
	@mkdir -p $(ROOT)/tmp
	bash scripts/stands/logs/check.sh

clean:
	rm -rf $(ROOT)
