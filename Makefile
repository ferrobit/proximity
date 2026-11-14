VERSION ?= 1.0.0
BINARY ?= bin/proximity
COMPOSE ?= scripts/proximity.sh

.PHONY: all web build test cover vet image up down restart logs password clean

all: web build

web:
	cd web && npm ci --no-audit --no-fund && npm run build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/proximity

vet:
	gofmt -l cmd internal web/embed.go
	go vet ./...

test:
	go test ./...

cover:
	go test -cover ./...

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

password:
	docker exec proximity cat /config/initial_admin_password.txt

clean:
	rm -rf bin web/node_modules
