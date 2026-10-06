GO            ?= go
IMAGE         ?= docker.io/yildizozan/jiramcp
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
HELM_CHART    ?= helm/jiramcp

.PHONY: all build dist test vet lint run docker helm-lint helm-template tidy clean

all: tidy vet test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/jiramcp ./cmd/server

# Cross-compiled binaries for developers' machines, with SHA-256 checksums.
DIST_PLATFORMS ?= darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64
dist:
	rm -rf dist && mkdir -p dist
	for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags "-s -w -X main.version=$(VERSION)" \
			-o dist/jiramcp_$(VERSION)_$${os}_$${arch}$$ext ./cmd/server || exit 1; \
	done
	cd dist && shasum -a 256 jiramcp_* > checksums.txt

test:
	$(GO) test ./... -race -count=1

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

run:
	$(GO) run ./cmd/server

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

helm-lint:
	helm lint $(HELM_CHART)

helm-template:
	helm template jiramcp $(HELM_CHART) \
		--set jira.baseUrl=https://jira.yildizozan.com \
		--set jira.secret.authEmail=svc@yildizozan.com \
		--set jira.secret.apiToken=token \
		--set mcp.authToken=mcp-secret

clean:
	rm -rf bin dist
