# jiramcp

An [MCP](https://modelcontextprotocol.io) server, written in Go with
[`mark3labs/mcp-go`](https://github.com/mark3labs/mcp-go), that creates **Jira
tickets on behalf of a named person**, routed to the **relevant team's**
project. All configuration is supplied via environment variables, and a Helm
chart is included for running it on Kubernetes.

## How "on behalf of" works

A single Jira **service account** (its credentials come from the environment)
performs the create call, but the issue's **reporter** is set to the named
person. This requires the service account to hold the **"Modify Reporter"**
permission in the target project. The server resolves the person to an
Atlassian `accountId` and validates — via the project's create-screen
metadata — that the reporter field is actually settable before creating;
if it is not, it returns a clear error instead of silently filing the ticket
as the service account.

> Two dialects, selected by `JIRA_AUTH_MODE`: **Jira Cloud** (REST v3, ADF,
> accountId identities) under email+token, and **Jira Server/Data Center** (REST
> v2, plain-text descriptions, username identities) under a Bearer PAT.

## Tools

| Tool | Purpose |
|---|---|
| `create_jira_ticket` | Create a ticket for a `team` (or explicit `project`) with `reporter` set to a named person. |
| `update_jira_ticket` | Edit fields (summary, description, assignee, priority, labels, components, due date) on an existing issue. |
| `add_comment` | Add a comment to an existing issue. |
| `list_comments` | List the newest comments on an issue (oldest first), with the `id` needed to edit one. |
| `update_comment` | Replace the body of an existing comment, by `comment_id`. |
| `transition_jira_ticket` | List the available workflow transitions for an issue, or apply one (optionally with a comment). |
| `search_users` | Resolve a name/email to the reporter/assignee id (accountId on Cloud, username on Server/DC). |
| `list_projects` | List projects reachable by the service account. |
| `list_issue_types` | List issue types valid for a project. |

`create_jira_ticket` parameters: `summary` (required), `reporter` (required —
accountId, or email/name resolved to exactly one active user), `team` and/or
`project`, `issue_type`, `description` (plain text → ADF), `assignee`,
`priority`, `labels`, `components`, `due_date` (`YYYY-MM-DD`), `parent`.

## Configuration (environment variables)

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `JIRA_BASE_URL` | yes | — | e.g. `https://jira.yildizozan.com` |
| `JIRA_AUTH_MODE` | no | inferred | `cloud` or `dc`; inferred `dc` when `JIRA_PAT` is set |
| `JIRA_AUTH_EMAIL` | cloud | — | Service-account email |
| `JIRA_API_TOKEN` | cloud | — | Service-account API token |
| `JIRA_PAT` | dc | — | Personal access token (Bearer) |
| `JIRA_TEAM_MAPPING_YAML` | one of | — | Inline team→project YAML (JSON also accepted) |
| `JIRA_TEAM_MAPPING_FILE` | one of | — | Path to mapping file (e.g. ConfigMap mount) |
| `JIRA_DEFAULT_ISSUE_TYPE` | no | `Task` | Fallback issue type |
| `JIRA_HTTP_TIMEOUT` | no | `15s` | Per-request timeout |
| `MCP_HTTP_ADDR` | no | `:8080` | HTTP listen address |
| `MCP_HTTP_PATH` | no | `/mcp` | MCP endpoint path |
| `MCP_AUTH_TOKEN` | http | — | Bearer token required to call the HTTP endpoint |
| `MCP_ALLOW_UNAUTHENTICATED` | no | `false` | Disable HTTP auth (dev only) |
| `HTTP_HEALTH_ADDR` | no | `:8081` | Health/readiness listen address |
| `LOG_LEVEL` | no | `info` | `debug`/`info`/`warn`/`error` |
| `LOG_FORMAT` | no | `json` | `json` or `text` |

### Team mapping

```yaml
teams:
  payments:
    projectKey: PAY
    defaultIssueType: Task
    components: [backend]
    labels: [from-mcp]
    fields:
      customfield_10010: { value: Payments }
  platform:
    projectKey: PLAT
defaultTeam: platform
```

`fields` lets you supply required custom fields per team. Team defaults for
`labels`/`components` are merged with any passed in the tool call. YAML is a
superset of JSON, so an inline JSON document is still accepted.

## Run locally

```bash
make build
JIRA_BASE_URL=https://jira.yildizozan.com \
JIRA_AUTH_EMAIL=svc@yildizozan.com JIRA_API_TOKEN=*** \
JIRA_TEAM_MAPPING_FILE=examples/team-mapping.yaml \
./bin/jiramcp          # stdio (same as: ./bin/jiramcp stdio)
```

The subcommand selects the transport: `jiramcp` or `jiramcp stdio` serves MCP
over stdio, `jiramcp http` serves streamable HTTP. The Docker image defaults to
`http`; the Helm chart passes `mcp.transport` as the subcommand.

In `http` mode the server requires `MCP_AUTH_TOKEN` (or set
`MCP_ALLOW_UNAUTHENTICATED=true` for local dev only). Health/readiness are on
`HTTP_HEALTH_ADDR` (`/healthz`, `/readyz`).

## Container

```bash
make docker IMAGE=docker.io/yildizozan/jiramcp VERSION=0.1.0
```

Multi-stage build (Chainguard `cgr.dev/chainguard/go` → `cgr.dev/chainguard/static`),
static `CGO_ENABLED=0` binary, runs as UID 65532.

## Kubernetes (Helm)

```bash
helm install jiramcp helm/jiramcp \
  --set jira.baseUrl=https://jira.yildizozan.com \
  --set jira.existingSecret=jiramcp-credentials \
  --set-file teamMapping.inlineYaml=examples/team-mapping.yaml
```

The recommended pattern is `jira.existingSecret` (works with the External
Secrets Operator) holding keys `JIRA_AUTH_EMAIL`, `JIRA_API_TOKEN` (or
`JIRA_PAT`) and `MCP_AUTH_TOKEN`. Alternatively let the chart create the Secret
from `jira.secret.*` / `mcp.authToken` (never commit real values).

Highlights: explicit `secretKeyRef` env wiring, config/secret checksum
annotations (pods roll on change), startup/liveness/readiness probes (readiness
reflects cached Jira reachability), non-root + read-only-rootfs + dropped
capabilities + seccomp `RuntimeDefault`, `automountServiceAccountToken: false`,
PodDisruptionBudget, and optional Ingress/HPA/NetworkPolicy. The HTTP transport
runs **stateless**, so replicas and the HPA need no sticky sessions.

## Security notes

- The HTTP MCP endpoint creates Jira issues and exposes user search — never
  expose it unauthenticated. Prefer OIDC/mTLS at the ingress/gateway in
  addition to the bearer token.
- Credentials are only ever read from the environment (injected from a Secret);
  they are never logged (the startup config dump is redacted).
- The team mapping is the authorization boundary for every issue operation:
  `create` only targets mapped projects, and `update`/`add_comment`/
  `list_comments`/`update_comment`/`transition` reject any issue key whose
  project is not in the mapping.
- Editing a comment is authorized by Jira alone: the service account can only
  change what its project permissions allow ('Edit Own Comments' vs 'Edit All
  Comments'). `update_comment` REPLACES the body, so the previous text is kept
  only in Jira's own edit history.

## Development

```bash
make all        # tidy, vet, test, build
make test       # go test ./... -race
make helm-lint helm-template
```

## Layout

```
cmd/server/         entrypoint + lifecycle
internal/config/    env + team-mapping loading/validation
internal/jira/      Cloud v3 client (createmeta, ADF, error mapping)
internal/mcpserver/ MCP server, tools, transports, auth middleware
internal/health/    cached liveness/readiness
helm/jiramcp        Helm chart
```
