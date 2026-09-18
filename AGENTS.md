# AGENTS.md

Guidance for AI coding agents working on **jiramcp**.

## What this is

A Model Context Protocol (MCP) server in Go (`github.com/mark3labs/mcp-go`) that
creates and updates **Jira tickets on behalf of a named person**, routed to a
team's project. All configuration comes from environment variables; a Helm chart
runs it on Kubernetes. Transports: streamable HTTP (default) and stdio.

It speaks **two Jira dialects**, selected by `JIRA_AUTH_MODE`:
- `cloud` — Jira Cloud, REST **v3**, **ADF** descriptions, **accountId** identity, Basic `email:token`.
- `dc` — Jira Server/Data Center, REST **v2**, **plain-text** descriptions, **username** identity, Bearer **PAT**.

## Build, test, run

`go` is **not on PATH**. Use the JetBrains SDK:

```sh
export GOROOT=/Users/ozan.yildiz/sdk/go1.26.4
export PATH=$GOROOT/bin:$PATH
export GOPATH=/Users/ozan.yildiz/go
go build ./...      # or: go build -o bin/jiramcp ./cmd/server
go vet ./...
gofmt -l internal cmd          # must be empty; gofmt -w to fix
go test ./... -race
```

`make all` (tidy, vet, test, build), `make docker`, `make helm-lint helm-template`
also work but assume `go` on PATH. The shell is **zsh**, which does NOT word-split
unquoted `$var` — use a zsh array or `${=var}` when looping over a file list.

Run locally over stdio against a real Jira:

```sh
JIRA_BASE_URL=https://jira.yildizozan.com JIRA_AUTH_MODE=dc JIRA_PAT=*** \
MCP_TRANSPORT=stdio JIRA_TEAM_MAPPING_FILE=examples/dosd-mapping.yaml \
./bin/jiramcp
```

## Git

**Commit after every change.** Make a focused commit for each logical change as
soon as it builds and tests pass (`gofmt -l` empty, `go vet ./...`, `go test ./...
-race`) — do not batch several unrelated edits into one commit or leave finished
work uncommitted. One change = one commit, with a message explaining the *why*.
End commit messages with:

```
Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```

## Layout

```
cmd/server/main.go        wiring: config -> jira client -> mcp server -> transport; /myself credential check
internal/config/          env parsing (config.go) + team mapping load/validate (teams.go, YAML)
internal/jira/            Client interface (client.go) + dual-dialect HTTP impl (cloud.go); ADF (adf.go); error mapping (errors.go)
internal/mcpserver/       MCP server, tools, transports, auth middleware
internal/health/          cached liveness/readiness
helm/jiramcp/             Helm chart (chart is at repo root, not under deploy/)
examples/                 *.yaml team-mapping examples
mcp.values.yaml           real deploy values (namespace mcp, host jiramcp.yildizozan.com)
```

## Tools (9)

`create_jira_ticket`, `update_jira_ticket`, `add_comment`, `list_comments`,
`update_comment` (replaces the body; id comes from `list_comments`),
`transition_jira_ticket` (omit `to` to list, pass `to` to apply),
`search_users`, `list_projects`, `list_issue_types`.

## Conventions & invariants (do not break)

- **Every HTTP path goes through `c.api(suffix)`** → `/rest/api/{2|3}{suffix}`.
  Never hardcode a version (a hardcoded `/rest/api/3/issue` was a real production bug).
- **`X-Atlassian-Token: no-check`** is sent on every request — Jira DC rejects mutating
  REST calls without it, returning an HTML page (JSON-decode `'<'` error). Redirects are
  NOT followed (`CheckRedirect` returns `ErrUseLastResponse`) so a login/XSRF 302 surfaces
  as a clear error instead of HTML.
- **Identity**: `User.Ref()` returns accountId (Cloud) or username (DC). `userRef()` wraps it
  as `{"accountId":…}` (Cloud) or `{"name":…}` (DC). Never expose/accept the DC user `key`
  as a reporter id — it is not valid in the `name` field.
- **Descriptions/comments**: `renderText()` → ADF on Cloud, plain string on DC. Reading
  back goes the other way: a comment body is an ADF tree on Cloud and a string on DC, so
  it is decoded through `wireComment` + `ADFToText`, never unmarshalled into a `string`.
- **`ListComments` pages to the END**: Jira returns comments oldest first, so the newest
  live on the last page. The total is read first and the window requested by offset;
  `orderBy` is NOT used (Server/DC ignores it and would silently return the oldest).
- **createmeta on DC FAILS OPEN**: `CreateMeta` returns an error in `dc` mode so the
  reporter is never blocked by a metadata pre-check (DC createmeta is unreliable; verified
  broken on the target). Validation is best-effort and skipped on error.
- **Team mapping is YAML** (`sigs.k8s.io/yaml` `UnmarshalStrict`): unknown fields rejected,
  team names lower-cased (duplicates rejected), and a team's `fields` may NOT contain
  reserved core fields (project/issuetype/summary/reporter/assignee/…). Inline JSON still
  parses (YAML superset). Env: `JIRA_TEAM_MAPPING_YAML` (inline) or `JIRA_TEAM_MAPPING_FILE`.
- Tool errors are returned as `mcp.NewToolResultError(...), nil` (not transport errors);
  successes use `mcp.NewToolResultStructured(...)`. HTTP transport runs stateless.

## Testing approach

- `internal/jira/cloud_test.go`: `httptest` servers asserting request path/method/headers/body
  per dialect (the load-bearing check that pins API version + payload shape). Add a DC-dialect
  test for any new client method.
- `internal/mcpserver/*_test.go`: handlers against `fakeClient` (in `tool_create_test.go`).
  Extend `fakeClient` when you add a `Client` method.
- `internal/config`: table/`t.Setenv` tests for env + mapping parsing/validation.

## Deployment

Image `docker.io/yildizozan/jiramcp` (build `--platform linux/amd64` on arm64 hosts).
Helm: `helm upgrade --install jiramcp helm/jiramcp -n mcp -f mcp.values.yaml`. Credentials
come from an existing k8s Secret via `jira.existingSecret` (keys `JIRA_PAT`,
`MCP_AUTH_TOKEN`); team mapping is rendered into a ConfigMap and mounted as
`/etc/jiramcp/team-mapping.yaml`. Bump `helm/jiramcp/Chart.yaml` `version`/`appVersion`
and push a new image tag so nodes pull fresh code (same tag is cached).

## Security

The HTTP MCP endpoint creates/edits Jira issues — never expose it unauthenticated
(`MCP_AUTH_TOKEN` bearer is mandatory unless `MCP_ALLOW_UNAUTHENTICATED=true` for local dev).
Credentials are read only from env and never logged. Do not commit real secrets or PATs.
