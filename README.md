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
| `get_jira_ticket` | Read an existing issue: summary, description (plain text), status, type, priority, people, labels, components, dates, parent. |
| `update_jira_ticket` | Edit fields (summary, description, assignee, priority, labels, components, due date) on an existing issue; `clear` empties assignee, due date or description. A new description replaces the old one as plain text, so Cloud rich formatting is lost. |
| `add_comment` | Add a comment to an existing issue. |
| `list_comments` | List the newest comments on an issue (oldest first), with the `id` needed to edit one. |
| `update_comment` | Replace the body of an existing comment, by `comment_id`. |
| `transition_jira_ticket` | List the available workflow transitions for an issue, or apply one (optionally with a comment). |
| `search_users` | Resolve a name/email to the reporter/assignee id (accountId on Cloud, username on Server/DC). |
| `list_projects` | List the projects the caller may use (see [Project access](#project-access)). |
| `list_issue_types` | List issue types valid for an allowed project. |

`create_jira_ticket` parameters: `summary` (required), `reporter` (user id, or
email/name resolved to exactly one active user; optional when the server runs
with your own token, where it defaults to you), `team` and/or `project`, `issue_type`, `description` (plain text → ADF), `assignee`,
`priority`, `labels`, `components`, `due_date` (`YYYY-MM-DD`), `parent`.

## Configuration (environment variables)

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `JIRA_BASE_URL` | yes | — | e.g. `https://jira.yildizozan.com` |
| `JIRA_AUTH_MODE` | no | inferred | `cloud` or `dc`; inferred `dc` when `JIRA_PAT` is set |
| `JIRA_AUTH_EMAIL` | cloud | — | Service-account email |
| `JIRA_API_TOKEN` | cloud | — | Service-account API token |
| `JIRA_PAT` | dc | — | Personal access token (Bearer) |
| `JIRA_PROJECTS` | no | — | Comma-separated allow-list of project keys, e.g. `PAY,DOSD` |
| `JIRA_DEFAULT_PROJECT` | no | — | Project used by create when no `project`/`team` is given |
| `JIRA_TEAM_MAPPING_YAML` | no | — | Inline team→project YAML (JSON also accepted) |
| `JIRA_TEAM_MAPPING_FILE` | no | — | Path to mapping file (e.g. ConfigMap mount) |
| `JIRA_DEFAULT_ISSUE_TYPE` | no | `Task` | Fallback issue type |
| `JIRA_HTTP_TIMEOUT` | no | `15s` | Per-request timeout |
| `MCP_HTTP_ADDR` | no | `:8080` | HTTP listen address |
| `MCP_HTTP_PATH` | no | `/mcp` | MCP endpoint path |
| `MCP_AUTH_TOKEN` | http | — | Bearer token required to call the HTTP endpoint |
| `MCP_ALLOW_UNAUTHENTICATED` | no | `false` | Disable HTTP auth (dev only) |
| `HTTP_HEALTH_ADDR` | no | `:8081` | Health/readiness listen address (`http` mode only) |
| `LOG_LEVEL` | no | `info` | `debug`/`info`/`warn`/`error` |
| `LOG_FORMAT` | no | `json` | `json` or `text` |

### Project access

Every issue operation is bounded by a project policy, resolved in this order:

1. `JIRA_PROJECTS`, when set: only those projects.
2. Otherwise the team mapping's projects, when a mapping is configured.
3. Otherwise every project the Jira token can see (stdio only).

The `http` service authenticates as a shared service account, so it refuses to
start unless `JIRA_PROJECTS` or a team mapping bounds it. Jira's own
permissions apply on top of the policy in every mode.

### Team mapping

The mapping is optional. It routes a `team` name to a project and supplies
per-team defaults; its projects form the policy when `JIRA_PROJECTS` is unset.

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

## Run locally (developer setup)

A developer runs the binary on their own machine with their **own** Jira
token. Tickets are filed as them (no Modify Reporter permission needed), and
they can work in every project Jira lets them into; no team mapping is needed.

```bash
# Download the binary for your OS from the GitHub release (or: make build),
# check it against checksums.txt, then register it with your MCP client:
claude mcp add jiramcp \
  -e JIRA_BASE_URL=https://jira.yildizozan.com \
  -e JIRA_PAT=<your personal access token> \
  -e JIRA_PROJECTS=PAY,DOSD \
  -e JIRA_DEFAULT_PROJECT=DOSD \
  -- /path/to/jiramcp
```

On macOS a downloaded binary is quarantined; clear it once with
`xattr -d com.apple.quarantine /path/to/jiramcp`.

`JIRA_PROJECTS` and `JIRA_DEFAULT_PROJECT` are optional: without the list every
project your token can see is usable, and without a default each create names
its `project`. On Jira Cloud use `JIRA_AUTH_EMAIL` + `JIRA_API_TOKEN` instead
of `JIRA_PAT`. Running stdio with a shared service-account token instead makes
that account the default reporter, so pass `reporter` explicitly in that case.

The subcommand selects the transport: `jiramcp` or `jiramcp stdio` serves MCP
over stdio, `jiramcp http` serves streamable HTTP. The Docker image defaults to
`http`; the Helm chart passes `mcp.transport` as the subcommand.

In `http` mode the server requires `MCP_AUTH_TOKEN` (or set
`MCP_ALLOW_UNAUTHENTICATED=true` for local dev only). Health/readiness are on
`HTTP_HEALTH_ADDR` (`/healthz`, `/readyz`); the health server runs only in
`http` mode, so several stdio instances can run side by side.

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
- The server does not know who calls it. One shared `MCP_AUTH_TOKEN` grants
  every caller the same rights, and `reporter` is taken from the tool input
  as given: anyone holding the token can file a ticket on behalf of any active
  user, and the logs record the reporter, not the caller. Treat the token as a
  service credential, give it only to trusted MCP clients, and rotate it when
  a client leaves. If you need per-caller accountability, put an identity-aware
  proxy (OIDC) in front of the endpoint and keep its access logs.
- Credentials are only ever read from the environment (injected from a Secret);
  they are never logged (the startup config dump is redacted).
- The project policy ([Project access](#project-access)) is the authorization
  boundary for every issue operation: `create` only targets allowed projects,
  and `get`/`update`/`add_comment`/`list_comments`/`update_comment`/
  `transition` reject any issue key whose project is not allowed. The check
  runs on the issue's current key as Jira reports it, so the old key of an
  issue moved out of an allowed project is rejected too. A `parent` passed to
  `create` must also be in an allowed project, and `list_projects`/
  `list_issue_types` only report allowed projects.
- `search_users` is the exception: it searches the whole Jira user directory
  (names and, where Jira shows them, emails), because a reporter or assignee
  can be anyone. Keep that in mind when deciding who gets the token.
- Editing a comment is authorized by Jira alone: the service account can only
  change what its project permissions allow ('Edit Own Comments' vs 'Edit All
  Comments'). `update_comment` REPLACES the body, so the previous text is kept
  only in Jira's own edit history.

## Development

```bash
make all        # tidy, vet, test, build
make test       # go test ./... -race
make helm-lint helm-template
make dist       # cross-compiled binaries + checksums in dist/
```

Pushing a `v*` tag runs `.github/workflows/release.yml`, which publishes the
`make dist` binaries as a GitHub release.

## Layout

```
cmd/server/         entrypoint + lifecycle
internal/config/    env + team-mapping loading/validation
internal/jira/      Cloud v3 client (createmeta, ADF, error mapping)
internal/mcpserver/ MCP server, tools, transports, auth middleware
internal/health/    cached liveness/readiness
helm/jiramcp        Helm chart
```
