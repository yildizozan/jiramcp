GO            ?= go
IMAGE         ?= ghcr.io/acme/jiramcp
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
HELM_CHART    ?= helm/jiramcp

.PHONY: all build test vet lint run docker helm-lint helm-template tidy clean

all: tidy vet test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/jiramcp ./cmd/server

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
		--set jira.baseUrl=https://acme.atlassian.net \
		--set jira.secret.authEmail=svc@acme.com \
		--set jira.secret.apiToken=token \
		--set mcp.authToken=mcp-secret

clean:
	rm -rf bin
