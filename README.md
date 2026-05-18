# Trello Copilot Gateway

Trello Copilot Gateway is a local Go service that turns Trello webhook events into dedicated GitHub Copilot worker sessions.

It receives signed Trello callbacks, verifies that they are authentic, routes each card event, and dispatches work to a per-card Copilot session. Each card is processed serially while different cards can run in parallel.

## What this project does

The project is an automation bridge for a Trello-based engineering workflow:

1. Trello sends card events to this gateway.
2. The gateway verifies the `X-Trello-Webhook` HMAC signature.
3. The event is reduced to routing-safe fields to limit prompt-injection surface.
4. The HCL-driven router classifies the card and decides whether to drop the event, spawn or notify a worker, or terminate a worker.
5. A Copilot worker session receives a system prompt assembled from playbook `.md` files (loaded from `--playbooks-dir`, with embedded skeleton prompts as a fallback) plus a CARD CONTEXT block carrying the resolved Trello list IDs and the per-card GitHub reference.
6. The worker follows the injected workflow guidance to analyze, plan, act, or cleanly hand off work.

The gateway is not a generic Trello-to-HTTP forwarder. It directly manages Copilot SDK sessions, worker lifecycle, card-specific queues, activity logs, and a small operator interface.

## Key features

- Trello webhook `HEAD /` validation support.
- Trello webhook `POST /` signature verification using `HMAC-SHA1(secret, raw_body + callbackURL)`.
- Minimal event payload extraction (slim payload) to limit prompt-injection surface.
- **Declarative HCL routing**: `kanban {}` describes the board roles, `rule {}` selects the per-card playbook, `github_issue(...)` HCL function extracts `(owner, repo, number, kind, url)` from the card's first description line. No hard-coded list names, no hard-coded work types. See [examples/router/README.md](./examples/router/README.md) for the full schema.
- **Startup-time kanban resolution**: human-readable list names in `router.hcl` are resolved against the configured Trello board via [go-trello-sdk](https://github.com/lonegunmanb/go-trello-sdk); the resolved list IDs are injected into every worker's CARD CONTEXT so prompts can reference roles by stable id.
- **On-disk playbooks with templating**: every `.md` under `--playbooks-dir` is copied into a per-process temp directory and any `{{<basename>}}` reference is rewritten to the absolute path of `<basename>` in that temp dir. Five skeleton prompts (`BOOTSTRAP`, `IDENTITY`, `WORKER`, `TOOLS`, `USER`) ship embedded as defaults and are overridden by same-name files on disk.
- **Per-card serialised dispatch**: one worker queue per card; cross-card events run in parallel.
- **Native Trello tooling**: workers get `trello_*` tools (card_get, card_list, board_lists, latest_comment, comments_since, comment, move) backed by the Go SDK — no PowerShell shell-out.
- **AzureRM provider work_dir hook**: when a per-card `work_dir` turns out to be a clone of `hashicorp/terraform-provider-azurerm`, the gateway synchronously clones `WodansSon/terraform-azurerm-ai-assisted-development` and runs its installer (pwsh on Windows, bash on macOS / Linux) before the worker starts. No spawned LLM session for the refresh.
- Terminal UI when stdin is a TTY; line-oriented REPL when running headless.
- Runtime log file written to `trellooperator.log` (mode `0o600`); operator secrets are redacted to length-only fingerprints.

## Repository layout

- [main.go](./main.go) — program entry point; wires every subpackage together.
- [internal/app/config.go](./internal/app/config.go) — CLI flag and environment configuration, with redaction of secrets.
- [internal/app/server.go](./internal/app/server.go) — Gin HTTP server and Trello webhook handling.
- [internal/app/verify.go](./internal/app/verify.go) — Trello signature verification.
- [internal/app/routing.go](./internal/app/routing.go) — event routing against the resolved kanban view.
- [internal/app/dispatcher.go](./internal/app/dispatcher.go) — per-card worker queues and lifecycle management.
- [internal/app/runner.go](./internal/app/runner.go) — Copilot SDK client, worker session construction, and CARD CONTEXT assembly.
- [internal/app/kanban/](./internal/app/kanban) — HCL `kanban {}` decoder + Trello list-id resolution.
- [internal/app/router/](./internal/app/router) — HCL `route {}` and `rule {}` engines, `github_issue` hclfunc.
- [internal/app/prompttmpl/](./internal/app/prompttmpl) — playbook renderer (`{{<basename>}}` substitution into a per-process temp dir).
- [internal/app/prompts/](./internal/app/prompts) — embedded skeleton playbooks (fallback when `--playbooks-dir` does not provide them).
- [internal/app/trelloclient/](./internal/app/trelloclient) — project-local wrapper around [go-trello-sdk](https://github.com/lonegunmanb/go-trello-sdk).
- [internal/app/aiassistedrefresh/](./internal/app/aiassistedrefresh) — synchronous Go-native replacement for the legacy `refresh-copilot-setup.ps1` wrapper.
- [internal/app/repl.go](./internal/app/repl.go) and [internal/app/tui.go](./internal/app/tui.go) — operator interfaces.
- [playbook/](./playbook) — the in-repo collection of `.md` playbooks; point `--playbooks-dir` at this directory to use them. (Gitignored under `playbook/*` — operators manage their own copies.)
- [examples/router/](./examples/router) — sample `router.hcl` and an annotated walkthrough of the HCL surface in [examples/router/README.md](./examples/router/README.md).

## Configuration

Both CLI flags and environment variables are supported. CLI flags take precedence over environment variables, which take precedence over defaults. **Every flag marked "required" must be set; startup fails fast with a clear error otherwise.**

| Flag | Environment variable | Default | Required | Description |
|---|---|---|---|---|
| `--listen` | `LISTEN_ADDR` | `:18790` | | HTTP listen address. |
| `--trello-api-secret` | `TRELLO_API_SECRET` | | **yes** | Trello API secret used for webhook signature verification (the value from your Trello webhook registration, NOT the API token). |
| `--trello-api-key` | `TRELLO_API_KEY` | | **yes** | Trello API key. The Go SDK authenticates every outbound Trello call (board lists, card reads, comments, list moves) with this key + token pair. |
| `--trello-api-token` | `TRELLO_API_TOKEN` | | **yes** | Trello API token. See above. |
| `--callback-url` | `CALLBACK_URL` | | **yes** | Public webhook callback URL registered in Trello. Must exactly match the URL Trello signs. |
| `--copilot-model` | `COPILOT_MODEL` | `claude-opus-4.6-1m` | | Copilot model name used for worker sessions. |
| `--router-dir` | `WORKSPACE_TRELLO_ROUTER_DIR` | `C:\Users\zjhe\.openclaw\workspace-trello-router` | **yes** | Directory containing `router.hcl` and the legacy `scripts/` helpers. |
| `--playbooks-dir` | `TRELLO_PLAYBOOKS_DIR` | `<cwd>/.playbooks` | **yes** | Directory containing the `.md` playbook files. Must exist and be a directory; missing files referenced via `{{<basename>}}` fail startup. |
| `--kanban-board-id` | `TRELLO_KANBAN_BOARD_ID` | | **yes** | Trello board id (24-hex string from the board URL). The kanban `{}` block in `router.hcl` is resolved against this board's lists at startup. |

## Quick start

### 1. Build

```bash
go build -o trello-copilot .
```

### 2. Prepare the router-dir and playbooks-dir

- Copy [examples/router/router.hcl](./examples/router/router.hcl) into `<router-dir>/router.hcl` and edit each `name = "..."` to match the open Trello list names on your board.
- Either point `--playbooks-dir` at this repo's [playbook/](./playbook) directory, or create your own directory with the playbooks you want (any `.md` files; see [examples/router/README.md](./examples/router/README.md) for the `{{<basename>}}` template syntax).

### 3. Run with environment variables

```bash
export LISTEN_ADDR=":18790"
export TRELLO_API_SECRET="your_trello_webhook_secret"
export TRELLO_API_KEY="your_trello_api_key"
export TRELLO_API_TOKEN="your_trello_api_token"
export CALLBACK_URL="https://your-public-domain/"
export COPILOT_MODEL="claude-opus-4.6-1m"
export WORKSPACE_TRELLO_ROUTER_DIR="C:\\Users\\zjhe\\.openclaw\\workspace-trello-router"
export TRELLO_PLAYBOOKS_DIR="C:\\project\\trello-openclaw-webhook-gateway\\playbook"
export TRELLO_KANBAN_BOARD_ID="64xxxxxxxxxxxxxxxxxxxxxx"

./trello-copilot
```

### 4. Or run with CLI flags

```bash
./trello-copilot \
  --listen ":18790" \
  --trello-api-secret "your_trello_webhook_secret" \
  --trello-api-key "your_trello_api_key" \
  --trello-api-token "your_trello_api_token" \
  --callback-url "https://your-public-domain/" \
  --copilot-model "claude-opus-4.6-1m" \
  --router-dir "C:\\Users\\zjhe\\.openclaw\\workspace-trello-router" \
  --playbooks-dir "C:\\project\\trello-openclaw-webhook-gateway\\playbook" \
  --kanban-board-id "64xxxxxxxxxxxxxxxxxxxxxx"
```

The gateway prints its full configuration (with `trello_api_secret`, `trello_api_key`, `trello_api_token` shown only as length fingerprints) at startup as `event=gateway_starting`.

## Request flow

### Trello validation

Trello sends a `HEAD /` request when a webhook is registered. The gateway returns `200 OK` so callback validation succeeds.

### Trello event delivery

For each `POST /` event, the gateway:

1. Reads the raw request body.
2. Verifies `X-Trello-Webhook` against `TRELLO_API_SECRET` and `CALLBACK_URL`.
3. Logs a human-readable event summary.
4. Immediately returns `202 Accepted` to Trello to avoid webhook retries.
5. Processes the event asynchronously through the Copilot runner and dispatcher.

### Routed payload shape

The prompt audit copy and worker event prompts use a slimmed JSON payload containing only routing-relevant fields.

Example:

```json
{
  "action": {
    "type": "updateCard",
    "data": {
      "card": { "id": "69ae188a" },
      "listBefore": { "name": "Backlog", "id": "x" },
      "listAfter": { "name": "Analyze", "id": "y" }
    }
  }
}
```

Free-form card text, comments, descriptions, board names, and member names are intentionally omitted from this slim payload.

## Worker routing summary

Routing is centered on Trello card IDs and the **resolved kanban view** produced at startup from `router.hcl`'s `kanban {}` block and the configured Trello board. The list-name knobs (`Analyze`, `In action`, `Done`, ...) shown below are the defaults in [examples/router/router.hcl](./examples/router/router.hcl); change them in your own `router.hcl` and they change everywhere.

- Moves into a `plan` or `action` role list (`Analyze`, `In action`) dispatch to a worker.
- Human comments dispatch to a worker.
- Agent comments whose trimmed text starts with any prefix in `kanban.agent_comment_prefixes` (default: `["[agent]:"]`) are dropped to avoid feedback loops.
- Moves to a `done` role list (`Done`) and card deletion terminate an existing worker.
- Moves to a `wait` role list (`Ready for plan review`, `Ready for review`, `Pending PR`, `Need Attention`) — **including any Trello list that no role claimed** — notify an existing worker to wind down; if no worker exists the event is dropped.
- Unsupported action types (`deleteComment`, anything else Trello may add in the future) are dropped.

## Operator interfaces

When stdin is a terminal, the gateway starts a full-screen TUI showing active workers, selected-card activity, and global events.

When stdin is not a terminal, it starts a simple REPL. Available commands include:

- `ls` — list active workers.
- `show <card_id>` — show detailed worker status and recent activity.
- `dump <card_id>` — print the worker activity log path.
- `help` — print command help.
- `quit` / `exit` — leave the REPL.

## Development and testing

```bash
go test ./...
go build ./...
```

Test coverage spans every package: configuration parsing, signature verification, slim-payload extraction, event routing against a kanban view, dispatcher lifecycle, HCL `kanban` / `route` / `rule` decoding and evaluation, the `github_issue` hclfunc, playbook templating, the AzureRM refresh hook, the Trello client wrapper, logging helpers, and both operator interfaces.

## Further reading

- [examples/router/README.md](./examples/router/README.md) — playbook templating rules, the `{{<basename>}}` syntax, and the full HCL router surface.
- [examples/router/router.hcl](./examples/router/router.hcl) — annotated reference `router.hcl` with every block type the gateway understands.
