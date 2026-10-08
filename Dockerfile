# syntax=docker/dockerfile:1

# Base images are pinned by digest so a build is reproducible and a changed
# upstream image cannot slip in unnoticed. Dependabot proposes digest bumps
# (.github/dependabot.yml); to bump by hand: crane digest cgr.dev/chainguard/<image>:latest

# ---- build ----
# Chainguard's Wolfi-based Go toolchain image. Build as root so the BuildKit
# cache mounts (/go, /root/.cache) are writable; this stage is not shipped.
FROM cgr.dev/chainguard/go:latest@sha256:aeecafdc18ad049656644ba7ff3bfac22490fc65a1656832c2bed9a005301dc2 AS build
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
FROM cgr.dev/chainguard/static:latest@sha256:fe55470f22d3259488d9d3739168d8f04da67755f0b69382bc26eda4a7d3d327
WORKDIR /
COPY --from=build /out/jiramcp /jiramcp
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/jiramcp"]
# The image targets Kubernetes, so it serves HTTP unless another subcommand is given.
CMD ["http"]
