# JJC — 军机处

> **JJC** (`JūnJīChù`, **军机处** — the *Grand Council*, the Qing-dynasty secretariat that helped the emperor decide every military and civil matter of state) is a small local Go service that runs a Trello board as the operating console for a fleet of GitHub Copilot worker sessions.
>
> Each Trello card is one piece of work. Humans drive the cards through the columns. JJC turns every column move and every comment into a **per-card Copilot session** that does the actual research, planning, code-writing, testing and PR review — then hands the result back as a Trello comment for the human to accept or redirect.
>
> The design goal, after the implementation pattern described in [*OpenClaw Trello Kanban Webhook Workflow Summary*](https://www.linkedin.com/pulse/openclaw-trello-kanban-webhook-workflow-summary-zijie-he-hbbgc/), is the asymmetric split:
>
> > **The Agent does 95% of the work; the human makes 100% of the decisions.**
>
> JJC is the *implementation* of that pattern with one deliberate simplification: there is no OpenClaw process in the loop. JJC speaks the [GitHub Copilot Go SDK](https://github.com/github/copilot-sdk) directly, so a Trello webhook becomes a Copilot turn with one HTTP hop in between.

---

## What it actually does

A single Go process binds three independent surfaces — Trello webhooks, the GitHub Copilot SDK, and the Trello REST API — into a per-card workflow engine:

1. **Webhook in.** Trello POSTs a signed webhook (`HMAC-SHA1(secret, raw_body + callbackURL)`) for every card create / update / move / comment / delete. JJC verifies the signature, immediately ACKs with `202`, and slims the event payload to the bare routing-safe fields (card id, list-after id, action type, comment text prefix-check) so nothing free-form from a human or an Issue body ever makes it into a prompt without going through a rule.
2. **HCL router decides.** A declarative `router.hcl` (a `kanban {}` block + ordered `route {}` and `rule {}` blocks; see [examples/router/router.hcl](./examples/router/router.hcl) and [examples/router/README.md](./examples/router/README.md)) classifies the event against the operator-configured board: drop / dispatch / notify_departure / terminate, plus which playbook(s) to inline as the worker's system prompt.
3. **Per-card Copilot session.** When a card needs a worker, JJC spawns or re-uses one Copilot SDK session **per card** (different cards run in parallel; the same card is strictly FIFO). The session's system prompt is assembled from:
   - the five embedded skeleton prompts ([BOOTSTRAP.md](./internal/app/prompts/BOOTSTRAP.md), [IDENTITY.md](./internal/app/prompts/IDENTITY.md), [WORKER.md](./internal/app/prompts/WORKER.md), [TOOLS.md](./internal/app/prompts/TOOLS.md), [USER.md](./internal/app/prompts/USER.md));
   - any operator-supplied playbook `.md` under `--playbooks-dir` (overrides same-name embeds);
   - a `# CARD CONTEXT` block with `card_id`, `work_dir`, `work_type`, `github_repo`, `github_number`, `github_url`, and the resolved Trello list IDs for every role;
   - the rule-selected entry playbook inlined verbatim.
4. **Worker acts.** The worker calls in-process Trello tools (`trello_card_get`, `trello_card_list`, `trello_card_comment`, `trello_card_move`, `trello_card_comments_since`, `trello_card_latest_comment`, `trello_board_lists`), reads the cloned repo at `<work_dir>`, drafts plans, runs experiments, opens PRs, and posts every conclusion back as a Trello comment prefixed with the operator-configured agent marker (default `[agent]:`).
5. **Human decides.** The column the human drags the card into IS the decision: `Analyze` → plan in progress; `Ready for plan review` → wait for human OK; `In action` → execute; `Ready for review` → wait for merge; `Pending PR` / `Need Attention` → wait / escalate; `Done` → terminate the session and tear down `work_dir`. The worker never auto-merges; it only proposes.

> JJC is not a generic Trello → HTTP forwarder. It owns Copilot session lifecycle, per-card FIFO queues, activity logs, `work_dir` preparation (`mkdir` + `git clone --depth 1` + registered hooks), and a small operator console.

---

## Why not OpenClaw?

The original workflow pattern (the LinkedIn article) used [OpenClaw](https://github.com/lonegunmanb/openclaw) as the AI-side process: OpenClaw managed a separate "manager" session that fanned out into per-card worker sessions, and the Trello bridge talked to OpenClaw over its HTTP API.

JJC removes that layer. There is no manager session, no second process, no IPC.

- JJC holds the Copilot SDK client directly (`github.com/github/copilot-sdk/go`).
- JJC holds the Trello SDK client directly ([`github.com/lonegunmanb/go-trello-sdk`](https://github.com/lonegunmanb/go-trello-sdk)).
- JJC maps cards → sessions in a single in-memory dispatcher ([internal/app/dispatcher.go](./internal/app/dispatcher.go)).
- JJC exposes a TUI / REPL for the operator on stdio.

The whole runtime is a single Go binary with no required external services beyond Copilot itself, Trello itself, and (optionally) a Cloudflare quick tunnel for local development.

---

## Key features

- **Trello webhook validation + signature verification.** `HEAD /` returns 200 for callback registration; `POST /` checks `X-Trello-Webhook` against `HMAC-SHA1(secret, raw_body + callbackURL)` ([internal/app/verify.go](./internal/app/verify.go)).
- **Slim payload extraction.** Free-form card text, comments, descriptions, board names and member names are dropped from the routing-time JSON; only id / type / list-after fields reach the router. Limits prompt-injection surface.
- **Declarative HCL routing.** `kanban {}` describes the board roles. `route {}` decides what to do with each webhook event. `rule {}` selects the per-card playbook. A `github_issue(...)` HCL function extracts `(owner, repo, number, kind, url)` from the card's first description line. No hard-coded list names anywhere in code. See [examples/router/README.md](./examples/router/README.md) for the full schema.
- **Startup kanban resolution.** Human-readable list names in `router.hcl` are resolved to stable Trello list IDs at startup against the configured board. The resolved IDs are injected into every worker's CARD CONTEXT (`kanban_plan_id`, `kanban_action_id`, `kanban_wait_plan_review_id`, …) so prompts and playbooks reference roles, not column names.
- **Playbook template rendering** ([docs/playbook-template-variables.md](./docs/playbook-template-variables.md)). Every `.md` under `--playbooks-dir` is copied into a per-process temp directory and pre-rendered:
  - `{{<basename>.md}}` cross-references resolve to absolute paths in the temp dir;
  - `{{kanban.<role>.id}}` / `{{kanban.<role>.name}}` / `{{kanban.<category>.list_ids}}` / `{{kanban.agent_comment_prefix}}` / `{{kanban.agent_comment_prefixes}}` resolve from the operator-configured `kanban {}` block (full schema in [docs/playbook-template-variables.md §2](./docs/playbook-template-variables.md));
  - unknown `{{kanban.*}}` keys are a startup-fatal `unknown_kanban_key` error, so renamed columns or typo'd keys surface at boot, not at the first worker turn.
- **Five embedded skeleton prompts** ship inside the binary as defaults and are overridden file-by-file by anything the operator drops into `--playbooks-dir`.
- **Per-card serialised dispatch.** One worker queue per card; cross-card events run in parallel ([internal/app/dispatcher.go](./internal/app/dispatcher.go)).
- **Native Trello tooling.** Workers get `trello_*` tools backed by the Go SDK — no PowerShell shell-out, no `curl` to `api.trello.com`.
- **AzureRM provider `work_dir` hook.** When a per-card `work_dir` turns out to be a clone of `hashicorp/terraform-provider-azurerm`, JJC synchronously clones `WodansSon/terraform-azurerm-ai-assisted-development` and runs its installer (pwsh on Windows, bash on macOS / Linux) before the worker starts. No spawned LLM session for the refresh ([internal/app/aiassistedrefresh/](./internal/app/aiassistedrefresh/)).
- **Cloudflared quick-tunnel by default.** With no `--tunnel` argument, JJC starts a TryCloudflare quick tunnel, waits for the public URL, reconciles the Trello board webhook to that URL, and uses the URL for signature verification. `--tunnel=none` opts out for production deployments with a stable public domain.
- **Operator console.** Full-screen TUI when stdin is a terminal; line-oriented REPL when running headless.
- **Audited runtime log.** `trellooperator.log` is written next to the binary at mode `0o600`; operator secrets are redacted to length-only fingerprints.

---

## Repository layout

| Path | What it owns |
|---|---|
| [main.go](./main.go) | Program entry; wires every subpackage together. |
| [internal/app/config.go](./internal/app/config.go) | CLI flag + environment configuration with secret-fingerprint redaction. |
| [internal/app/server.go](./internal/app/server.go) | Gin HTTP server, Trello webhook handler, body-size limits, signature checking. |
| [internal/app/verify.go](./internal/app/verify.go) | Trello `HMAC-SHA1` signature verification. |
| [internal/app/slim.go](./internal/app/slim.go) | Slim-payload extractor (prompt-injection surface reduction). |
| [internal/app/dispatcher.go](./internal/app/dispatcher.go) | Per-card worker queues + lifecycle. |
| [internal/app/runner.go](./internal/app/runner.go) | Copilot SDK client, worker session construction, CARD CONTEXT assembly. |
| [internal/app/kanban/](./internal/app/kanban/) | HCL `kanban {}` decoder + Trello list-id resolution + `PromptVars()` schema + `ActiveAgentCommentPrefix()` helper. |
| [internal/app/router/](./internal/app/router/) | HCL `route {}` and `rule {}` engines, `github_issue` hclfunc. |
| [internal/app/prompttmpl/](./internal/app/prompttmpl/) | Per-process temp dir + strict `{{<basename>.md}}` / `{{kanban.*}}` renderer. |
| [internal/app/prompts/](./internal/app/prompts/) | Embedded skeleton playbooks (`BOOTSTRAP` / `IDENTITY
` / `WORKER` / `TOOLS` / `USER`). |
| [internal/app/trelloclient/](./internal/app/trelloclient/) | Project-local wrapper around [go-trello-sdk](https://github.com/lonegunmanb/go-trello-sdk). |
| [internal/app/aiassistedrefresh/](./internal/app/aiassistedrefresh/) | Synchronous Go-native AzureRM refresh hook. |
| [internal/app/trello_tools.go](./internal/app/trello_tools.go) | The seven `trello_*` SDK tools registered on every worker session. |
| [internal/app/tunnel/](./internal/app/tunnel/) | Cloudflared quick-tunnel provider. |
| [internal/app/repl.go](./internal/app/repl.go), [internal/app/tui.go](./internal/app/tui.go) | Operator interfaces. |
| [docs/playbook-template-variables.md](./docs/playbook-template-variables.md) | Authoring contract for `{{kanban.*}}` template variables. |
| [examples/router/](./examples/router/) | Sample `router.hcl` and an annotated walkthrough of the HCL surface. |
| [playbook/](./playbook/) | In-repo collection of `.md` playbooks (gitignored under `playbook/*` — operators manage their own copies; point `--playbooks-dir` at this directory to use them). |

---

## Configuration

Both CLI flags and environment variables are supported. CLI flags take precedence over environment variables, which take precedence over defaults. **Every flag marked "required" must be set; startup fails fast with a clear error otherwise.**

| Flag | Environment variable | Default | Required | Description |
|---|---|---|---|---|
| `--listen` | `LISTEN_ADDR` | `:18790` | | HTTP listen address. |
| `--trello-api-secret` | `TRELLO_API_SECRET` | | **yes** | Trello API secret used for webhook signature verification (the value from your Trello webhook registration, NOT the API token). |
| `--trello-api-key` | `TRELLO_API_KEY` | | **yes** | Trello API key. The Go SDK authenticates every outbound Trello call (board lists, card reads, comments, list moves) with this key + token pair. |
| `--trello-api-token` | `TRELLO_API_TOKEN` | | **yes** | Trello API token. See above. |
| `--callback-url` | `CALLBACK_URL` | | only with `--tunnel=none` | Public webhook callback URL registered in Trello. Must exactly match the URL Trello signs. Rejected with the default auto-tunnel path. |
| `--tunnel` | `TRELLO_GATEWAY_TUNNEL` | `cloudflared` | | Tunnel provider. `cloudflared` starts a TryCloudflare quick tunnel, waits for its public URL, reconciles the Trello board webhook, and uses that URL for signature verification. `none` disables auto-tunnel/webhook management and requires `--callback-url`. |
| `--copilot-model` | `COPILOT_MODEL` | `claude-opus-4.6-1m` | | Copilot model name used for worker sessions. |
| `--router-dir` | `WORKSPACE_TRELLO_ROUTER_DIR` | | **yes** | Directory containing `router.hcl`. |
| `--playbooks-dir` | `TRELLO_PLAYBOOKS_DIR` | `<cwd>/.playbooks` | **yes** | Directory containing the `.md` playbook files. Must exist and be a directory; missing files referenced via `{{<basename>}}` fail startup; unknown `{{kanban.*}}` keys fail startup. |
| `--kanban-board-id` | `TRELLO_KANBAN_BOARD_ID` | | **yes** | Trello board id (24-hex string from the board URL). The `kanban {}` block in `router.hcl` is resolved against this board's lists at startup. |

---

## Quick start

### 1. Install

```bash
go install github.com/lonegunmanb/jjc@latest
```

The binary is named `jjc`.

### 2. Prepare the router-dir and playbooks-dir

- Copy [examples/router/router.hcl](./examples/router/router.hcl) into `<router-dir>/router.hcl` and edit each `name = "..."` to match the open Trello list names on your board (or change `agent_comment_prefixes` to a non-default marker if you want — every prompt reads the active prefix from `{{kanban.agent_comment_prefix}}` at render time).
- Either point `--playbooks-dir` at this repo's [playbook/](./playbook/) directory, or create your own directory with the playbooks you want. Any `.md` files are accepted; see [docs/playbook-template-variables.md](./docs/playbook-template-variables.md) for the full `{{kanban.*}}` schema and [examples/router/README.md](./examples/router/README.md) for the cross-playbook `{{<basename>}}` syntax.

### 3. Run with environment variables (default quick tunnel)

```bash
export LISTEN_ADDR=":18790"
export TRELLO_API_SECRET="your_trello_webhook_secret"
export TRELLO_API_KEY="your_trello_api_key"
export TRELLO_API_TOKEN="your_trello_api_token"
export COPILOT_MODEL="claude-opus-4.6-1m"
export WORKSPACE_TRELLO_ROUTER_DIR="/path/to/your/router-dir"
export TRELLO_PLAYBOOKS_DIR="/path/to/your/playbooks-dir"
export TRELLO_KANBAN_BOARD_ID="64xxxxxxxxxxxxxxxxxxxxxx"

jjc
```

With no `--tunnel` argument, JJC uses the dev-friendly default `--tunnel=cloudflared`: it starts `cloudflared tunnel --url http://localhost:<listen-port>`, waits for the `https://*.trycloudflare.com/` URL, updates or creates the Trello webhook for `TRELLO_KANBAN_BOARD_ID`, and uses that exact URL for webhook signature verification. Install `cloudflared` first, or opt out with `--tunnel=none` when you manage a stable public URL yourself.

### 4. Or run with CLI flags

```bash
jjc \
  --listen ":18790" \
  --trello-api-secret "your_trello_webhook_secret" \
  --trello-api-key "your_trello_api_key" \
  --trello-api-token "your_trello_api_token" \
  --copilot-model "claude-opus-4.6-1m" \
  --router-dir "/path/to/your/router-dir" \
  --playbooks-dir "/path/to/your/playbooks-dir" \
  --kanban-board-id "64xxxxxxxxxxxxxxxxxxxxxx"
```

### 5. Production opt-out with a stable callback URL

Use `--tunnel=none` when a public domain already points at the gateway and you manage the Trello webhook callback manually:

```bash
jjc \
  --listen ":18790" \
  --trello-api-secret "your_trello_webhook_secret" \
  --trello-api-key "your_trello_api_key" \
  --trello-api-token "your_trello_api_token" \
  --tunnel "none" \
  --callback-url "https://your-public-domain/" \
  --copilot-model "claude-opus-4.6-1m" \
  --router-dir "/path/to/your/router-dir" \
  --playbooks-dir "/path/to/your/playbooks-dir" \
  --kanban-board-id "64xxxxxxxxxxxxxxxxxxxxxx"
```

JJC prints its full configuration (with `trello_api_secret`, `trello_api_key`, `trello_api_token` shown only as length fingerprints) at startup as `event=gateway_starting`.

---

## Request flow

### Trello validation

Trello sends a `HEAD /` request when a webhook is registered. JJC returns `200 OK` so callback validation succeeds.

### Trello event delivery

For each `POST /` event, JJC:

1. Reads the raw request body.
2. Verifies `X-Trello-Webhook` against `TRELLO_API_SECRET` and the in-memory callback URL (the auto-tunnel URL by default, or `--callback-url` when `--tunnel=none`).
3. Logs a human-readable event summary plus a structured slim-payload audit copy.
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

---

## Worker routing summary

Routing is centered on Trello card IDs and the **resolved kanban view** produced at startup from `router.hcl`'s `kanban {}` block and the configured Trello board. The list-name knobs (`Analyze`, `In action`, `Done`, …) shown below are the defaults in [examples/router/router.hcl](./examples/router/router.hcl); change them in your own `router.hcl` and they change everywhere (the playbook renderer pre-substitutes `{{kanban.<role>.name}}` to whatever you configured).

- Moves into a `plan` or `action` role list (`Analyze`, `In action`) dispatch to a worker.
- Human comments dispatch to a worker.
- Agent comments whose trimmed text starts with any prefix in `kanban.agent_comment_prefixes` (default: `["[agent]:"]`) are dropped to avoid feedback loops.
- Moves to a `done` role list (`Done`) and card deletion terminate an existing worker.
- Moves to a `wait` role list (`Ready for plan review`, `Ready for review`, `Pending PR`, `Need Attention`) — **including any Trello list that no role claimed** — notify an existing worker to wind down; if no worker exists the event is dropped.
- Unsupported action types (`deleteComment`, anything else Trello may add in the future) are dropped.

---

## Operator interfaces

When stdin is a terminal, JJC starts a full-screen TUI showing active workers, selected-card activity, and global events.

When stdin is not a terminal, it starts a simple REPL. Available commands include:

- `ls` — list active workers.
- `show <card_id>` — show detailed worker status and recent activity.
- `dump <card_id>` — print the worker activity log path.
- `help` — print command help.
- `quit` / `exit` — leave the REPL.

---

## Development and testing

```bash
go test ./...
go build ./...
```

Test coverage spans every package: configuration parsing, signature verification, slim-payload extraction, event routing against a kanban view, dispatcher lifecycle, HCL `kanban` / `route` / `rule` decoding and evaluation, the `github_issue` hclfunc, playbook templating (including strict-mode rejection of unknown `{{kanban.*}}` keys and end-to-end render guards on the embedded prompts), the AzureRM refresh hook, the Trello client wrapper, logging helpers, and both operator interfaces.

---

## Further reading

- [docs/playbook-template-variables.md](./docs/playbook-template-variables.md) — the complete authoring contract for `{{kanban.*}}` template variables in playbooks and embedded prompts (schema, exceptions, mechanical conversion table, anti-patterns).
- [examples/router/README.md](./examples/router/README.md) — playbook layout, `{{<basename>}}` cross-reference syntax, and the full HCL router surface.
- [examples/router/router.hcl](./examples/router/router.hcl) — annotated reference `router.hcl` with every block type JJC understands.
- [*OpenClaw Trello Kanban Webhook Workflow Summary*](https://www.linkedin.com/pulse/openclaw-trello-kanban-webhook-workflow-summary-zijie-he-hbbgc/) — the article describing the workflow pattern JJC implements (with the OpenClaw layer removed).