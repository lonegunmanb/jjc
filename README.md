# Trello Copilot Gateway

Trello Copilot Gateway is a local Go service that turns Trello webhook events into dedicated GitHub Copilot worker sessions.

It receives signed Trello callbacks, verifies that they are authentic, routes each card event, and dispatches work to a per-card Copilot session. Each card is processed serially while different cards can run in parallel.

## What this project does

The project is an automation bridge for a Trello-based engineering workflow:

1. Trello sends card events to this gateway.
2. The gateway verifies the `X-Trello-Webhook` HMAC signature.
3. The event is reduced to routing-safe fields to limit prompt-injection surface.
4. Routing decides whether to drop the event, spawn or notify a worker, notify an existing worker that a card left an active list, or terminate a worker.
5. A Copilot worker session receives a system prompt assembled from embedded prompt files plus card metadata.
6. The worker follows the injected workflow guidance to analyze, plan, act, or cleanly hand off work.

The gateway is not a generic Trello-to-HTTP forwarder anymore. It directly manages Copilot SDK sessions, worker lifecycle, card-specific queues, activity logs, and a small operator interface.

## Key features

- Trello webhook `HEAD /` validation support.
- Trello webhook `POST /` signature verification using `HMAC-SHA1(secret, raw_body + callbackURL)`.
- Minimal event payload extraction for safer prompt assembly.
- Deterministic event routing for Trello card moves, comments, creation, deletion, and terminal events.
- One worker queue per Trello card, with same-card events processed in order.
- Dedicated Copilot worker sessions created lazily per active card.
- Worker system prompt composition from embedded `BOOTSTRAP`, `IDENTITY`, `WORKER`, `TOOLS`, `USER`, and card context sections.
- Optional work-type pre-classification from Trello card metadata and router playbooks.
- Terminal UI when stdin is a TTY; line-oriented REPL when running headless.
- Runtime log file written to `trellooperator.log`.

## Repository layout

- `main.go` — program entry point.
- `internal/app/config.go` — CLI flag and environment configuration.
- `internal/app/server.go` — Gin HTTP server and Trello webhook handling.
- `internal/app/verify.go` — Trello signature verification.
- `internal/app/routing.go` — Trello event routing rules.
- `internal/app/dispatcher.go` — per-card worker queues and lifecycle management.
- `internal/app/runner.go` — Copilot SDK client and worker session creation.
- `internal/app/prompts/` — embedded base prompt fragments.
- `internal/app/repl.go` and `internal/app/tui.go` — operator interfaces.

## Configuration

Both CLI flags and environment variables are supported. CLI flags take precedence.

| Flag | Environment variable | Default | Description |
|---|---|---|---|
| `--listen` | `LISTEN_ADDR` | `:18790` | HTTP listen address. |
| `--trello-api-secret` | `TRELLO_API_SECRET` | required | Trello API secret used for webhook signature verification. |
| `--callback-url` | `CALLBACK_URL` | required | Public webhook callback URL registered in Trello. It must exactly match the URL Trello signs. |
| `--copilot-model` | `COPILOT_MODEL` | `claude-opus-4.6-1m` | Copilot model name used for worker sessions. |
| `--router-dir` | `WORKSPACE_TRELLO_ROUTER_DIR` | `C:\Users\zjhe\.openclaw\workspace-trello-router` | Directory containing router playbooks and Trello helper scripts. |

## Quick start

### 1. Build

```bash
go build -o trello-copilot .
```

### 2. Run with environment variables

```bash
export LISTEN_ADDR=":18790"
export TRELLO_API_SECRET="your_trello_api_secret"
export CALLBACK_URL="https://your-public-domain/"
export COPILOT_MODEL="claude-opus-4.6-1m"
export WORKSPACE_TRELLO_ROUTER_DIR="C:\\Users\\zjhe\\.openclaw\\workspace-trello-router"

./trello-copilot
```

### 3. Run with CLI flags

```bash
./trello-copilot \
  --listen ":18790" \
  --trello-api-secret "your_trello_api_secret" \
  --callback-url "https://your-public-domain/" \
  --copilot-model "claude-opus-4.6-1m" \
  --router-dir "C:\\Users\\zjhe\\.openclaw\\workspace-trello-router"
```

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

Routing is centered on Trello card IDs:

- Moves into active lists (`Analyze`, `In action`) dispatch to a worker.
- Human comments dispatch to a worker.
- Agent comments beginning with `[agent]:` are dropped to avoid feedback loops.
- Moves to `Done` and card deletion terminate an existing worker.
- Moves to non-active, non-terminal lists notify an existing worker to wind down, but do not spawn a new worker.
- Unsupported or unrelated events are dropped.

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

The test suite covers configuration, signature verification, event message generation, routing, dispatching, prompt embedding, worker bootstrap behavior, logging helpers, and the operator interfaces.
