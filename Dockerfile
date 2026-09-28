# syntax=docker/dockerfile:1

# ---- build ----
# Chainguard's Wolfi-based Go toolchain image. Build as root so the BuildKit
# cache mounts (/go, /root/.cache) are writable; this stage is not shipped.
FROM cgr.dev/chainguard/go:latest AS build
USER root
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOPATH=/go GOCACHE=/root/.cache/go-build

# Cache modules first.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build \
    -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/jiramcp ./cmd/server

# ---- runtime ----
# Chainguard's minimal static base (distroless-style): no shell/package manager,
# ships ca-certificates + tzdata, and defaults to the nonroot user 65532.
FROM cgr.dev/chainguard/static:latest
WORKDIR /
COPY --from=build /out/jiramcp /jiramcp
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/jiramcp"]
# The image targets Kubernetes, so it serves HTTP unless another subcommand is given.
CMD ["http"]
