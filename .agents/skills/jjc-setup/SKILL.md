---
name: jjc-setup
description: End-to-end install + configuration of JJC. Covers runtime deps (Go, `git`, `cloudflared`), `go install github.com/lonegunmanb/jjc@latest`, provisioning the Trello board + API key / token / OAuth secret, exporting trello environment variables and configuring the model, preparing `router.hcl` + playbooks, and verifying the first start. Use this skill whenever the user asks to install / set up / bootstrap / onboard / configure JJC, or hits startup errors like `copilot model … is not available`, missing env vars, Trello signature validation failures, or `cloudflared` not found.
---

# JJC Setup

End-to-end installer / configurator for the JJC gateway.

Authoritative reference for the binary, flags and runtime layout is the
top-level [README](../../README.md). This skill is the **operational
walkthrough** that turns that reference into a sequenced, copy-pasteable
setup on a fresh box.

---

## When to use this skill

Trigger this skill when the user wants to:

- install `jjc` for the first time;
- rebuild after pulling a new revision;
- provision the Trello board, API key, API token, and webhook secret;
- install or upgrade runtime dependencies (Go, `git`, `cloudflared`,
  GitHub Copilot CLI auth);
- export the required environment variables for a shell session or a
  service supervisor;
- copy `examples/router/router.hcl` into a real `--router-dir` and point
  `--playbooks-dir` at a valid playbooks directory;
- diagnose a failed first start (model-not-available, missing env vars,
  Trello 401, `cloudflared` not on PATH, etc.).

If the user only wants to *use* an already-running JJC (drag cards,
write playbooks, debug a worker), prefer the in-repo docs over this
skill.

---

## Setup flow at a glance

1. **Prerequisites** — OS-level tools the rest of the steps assume.
2. **Trello side** — board, API key, token, webhook secret, board id.
3. **GitHub Copilot side** — auth + model availability check.
4. **Build / install the binary** — from source or `go install`.
5. **Router + playbooks** — `router.hcl` and `--playbooks-dir` content.
6. **Environment variables** — the full set, with safe-handling notes.
7. **First run** — quick-tunnel vs `--tunnel=none`, expected log lines.
8. **Verification** — what a healthy startup looks like; common errors.

Each numbered step has a dedicated section below. Work through them in
order on a fresh machine; for partial re-installs, jump to the relevant
section.

---

## 1. Prerequisites

JJC is a Go binary that shells out to `git`, the Copilot SDK, and
(optionally) `cloudflared`. Before any other step, make sure each of
the following is installed and discoverable on `PATH`:

| Tool          | Why JJC needs it                                                    | Verify with                  |
| ------------- | ------------------------------------------------------------------- | ---------------------------- |
| Go toolchain  | Build / `go install` the `jjc` binary                               | `go version`                 |
| `git`         | Per-card `work_dir` is created via `git clone --depth 1`            | `git --version`              |
| `cloudflared` | Default `--tunnel=cloudflared` quick-tunnel provider                | `cloudflared --version`      |
| PowerShell 7+ | AzureRM refresh hook installer on Windows                           | `pwsh --version`             |
| `bash` + coreutils | AzureRM refresh hook installer on macOS / Linux                | `bash --version`             |

Deep-dive references (one per tool; only added as needed):

- **Go toolchain + `go install` PATH wiring** —
  see [references/install-go.md](references/install-go.md). Covers per-OS
  install, `GOBIN` / `GOPATH` resolution, adding the install dir to
  `PATH`, and a `go install golang.org/x/example/hello@latest`
  round-trip check.
- `git` — TBD.
- **`cloudflared`** — see
  [references/install-cloudflared.md](references/install-cloudflared.md).
  Covers the "is it already installed" probe (`cloudflared --version`),
  per-OS install (Windows MSI, macOS Homebrew, Linux package /
  one-liner), the TryCloudflare quick-tunnel sanity check, and the
  `--tunnel=none` opt-out.
- PowerShell 7+ on Windows — TBD.
- `bash` + POSIX tools on macOS / Linux — TBD.

## 2. Trello side: board, keys, webhook secret

JJC needs four Trello values exported to the launching shell:

| Env var                  | What it is                                                                 |
| ------------------------ | -------------------------------------------------------------------------- |
| `TRELLO_API_KEY`         | API key of the developer Power-Up the operator owns                        |
| `TRELLO_API_TOKEN`       | OAuth token the Power-Up uses to read / write the operator's boards        |
| `TRELLO_API_SECRET`      | "OAuth secret" of the same Power-Up — used to HMAC-SHA1 sign webhooks      |
| `TRELLO_KANBAN_BOARD_ID` | 24-hex id of the kanban board JJC drives                                   |

**Always detect first, ask second.** Before walking the user through
account / Power-Up setup, probe the current shell:

PowerShell:

```powershell
'TRELLO_API_KEY','TRELLO_API_TOKEN','TRELLO_API_SECRET','TRELLO_KANBAN_BOARD_ID' |
    ForEach-Object {
        $v = [Environment]::GetEnvironmentVariable($_)
        if ([string]::IsNullOrEmpty($v)) { "$_ MISSING" } else { "$_ set (len=$($v.Length))" }
    }
```

bash / zsh:

```bash
for v in TRELLO_API_KEY TRELLO_API_TOKEN TRELLO_API_SECRET TRELLO_KANBAN_BOARD_ID; do
    if [ -z "${!v}" ]; then echo "$v MISSING"; else echo "$v set (len=${#!v})"; fi
done
```

Branch on the result:

1. **All four present** → skip to [§6](#6-environment-variables) and
   confirm they're wired into the shell that will launch `jjc`.
2. **One or more missing** → ask:

   > Do you already have a Trello account?

   - **No** → send the user to **<https://trello.com/signup>** to
     create one (free tier is enough). Then create a workspace and a
     board (any template — JJC re-purposes the columns through
     `router.hcl`). After that, fall through to the "Yes" branch.
   - **Yes** → next ask, one at a time, whether they already have each
     of the three secrets. Use the plain-language explanations below so
     a user who has never touched Trello's developer surface can answer
     confidently. **Do not ask them to paste any value yet** — just
     find out which ones already exist; the actual export step is §6.

     > **Do you already have a Trello API key?**
     >
     > The API key is a 32-character hex string that identifies a
     > "Power-Up" (Trello's word for a small app you register against
     > your account). It is *not* your Trello login password and *not*
     > the board URL. If you've never opened
     > <https://trello.com/power-ups/admin> and clicked **New**, you
     > don't have one yet — that's normal.

     > **Do you already have a Trello API token?**
     >
     > The API token is a long opaque string (~64+ characters) that
     > grants the Power-Up permission to read and write *your* boards
     > on your behalf. It's generated by clicking the **Token** link
     > next to the API key and approving the prompt — Trello shows the
     > token once on the redirect page. If you've never approved that
     > prompt for this Power-Up, you don't have one yet.

     > **Do you already have a Trello "OAuth secret" (a.k.a. the
     > webhook signing secret)?**
     >
     > This is a second value displayed on the same Power-Up page,
     > directly under the API key. JJC uses it to verify that incoming
     > webhook POSTs really came from Trello (Trello signs every
     > webhook body with `HMAC-SHA1(secret, body + callback_url)`).
     > Even if you have an API key, you usually still need to scroll
     > down on the Power-Up page to copy this value.

     For each "Yes", trust the user and continue. For each "No / not
     sure", follow the developer-Power-Up walkthrough in
     [references/trello-credentials.md §4](references/trello-credentials.md)
     — the same flow produces all three values in one sitting, so it's
     fine to run it even when only one piece is missing.

   > **Collect the values one at a time.** When the walkthrough
   > produces each secret (API key, then OAuth secret, then API token),
   > prompt the user for **that one value only** and wait for their
   > reply before asking for the next one. Do **not** ask them to paste
   > all three (or all four, including the board id) in a single
   > message — most users are reading the Trello page one field at a
   > time, and a single combined prompt almost always comes back
   > partial or with values swapped. One question, one value, one
   > acknowledgement, then move on.

Once every value exists, jump to §6 to export them safely.

Deep-dive walkthrough — including the developer Power-Up flow at
<https://trello.com/power-ups/admin>, how to capture the 24-hex board
id from `…/<slug>.json`, safe export patterns, and a `curl` /
`Invoke-RestMethod` sanity probe — is in
[references/trello-credentials.md](references/trello-credentials.md).

> **No Trello board yet?** A brand-new account, or an existing
> account that has never been used for kanban work, won't have a
> `TRELLO_KANBAN_BOARD_ID` to capture. Ask the user:
>
> > Would you like JJC to create a default board for you? It will use
> > the API key + token from above to provision a board with seven
> > pre-named lists (`Need Attention`, `Pending PR`, `Analyze`,
> > `Ready for plan review`, `In action`, `Ready for review`, `Done`),
> > drop the matching `router.hcl` next to it, and persist both
> > `TRELLO_KANBAN_BOARD_ID` and `WORKSPACE_TRELLO_ROUTER_DIR` for
> > you.
>
> If yes, follow
> [references/bootstrap-board-and-router.md](references/bootstrap-board-and-router.md)
> end-to-end — that procedure subsumes §5 (router + playbooks) **and**
> half of §6 (it persists the two new env vars itself), so the next
> stop after a successful bootstrap is whichever of `COPILOT_MODEL` /
> `TRELLO_PLAYBOOKS_DIR` / `--callback-url` are still missing.

## 3. GitHub Copilot side

TBD — cover:
- which Copilot entitlement / org membership is required;
- how to authenticate the Copilot CLI / SDK on the host;
- how to confirm the configured `COPILOT_MODEL` is in the per-account
  `available models` list (see the
  `event=copilot_model_not_available` log line for the truth source).

## 4. Build or install the binary

Once Go is installed and the `go install` bin directory is on `PATH`
(see [§1 Prerequisites](#1-prerequisites) and
[references/install-go.md](references/install-go.md)), installing or
upgrading JJC is **always a single command**:

```bash
go install github.com/lonegunmanb/jjc@latest
```

Rules of thumb:

- **Whenever the user asks to install JJC, run this exact command** —
  the `@latest` suffix is intentional and means "always pull the most
  recent tagged release". Do not pin to a specific version unless the
  user explicitly asks for one.
- Re-running the same command upgrades the binary in place.
- The resulting executable is named `jjc` (or `jjc.exe` on Windows) and
  lands in `$GOBIN` / `$(go env GOPATH)/bin`, which §1 has already put
  on `PATH`. Confirm with:

  ```bash
  jjc --help
  ```

  If the shell can't find `jjc` after a successful `go install`, the
  `PATH` step from [references/install-go.md §3](references/install-go.md)
  was skipped — fix that first; do not fall back to absolute paths.

Only deviate from `go install … @latest` in these specific cases:

- **Local fork / unmerged branch.** Clone the fork and run
  `go install ./` from the repo root.
- **Air-gapped or proxy-blocked host.** Build on a connected machine
  with `go build -o jjc ./` (or `go build -o jjc.exe ./` on Windows)
  and copy the resulting binary into the target's `PATH`.

## 5. Router and playbooks directories

JJC needs two directories pointed at by env vars before it will start:

| Env var | Flag | Contents |
|---|---|---|
| `WORKSPACE_TRELLO_ROUTER_DIR` | `--router-dir`     | Must contain a single `router.hcl` (the `kanban {}` + `route {}` + `rule {}` declarations). |
| `TRELLO_PLAYBOOKS_DIR`        | `--playbooks-dir`  | Directory of `.md` playbooks selected by the `rule {}` blocks; the renderer reads only `.md` files at the top level. |

Two provisioning paths, depending on whether §2 already ran the
bootstrap procedure:

1. **Bootstrap path (recommended for new operators).** If §2 ended in
   the "no Trello board yet" branch, the operator should have followed
   [references/bootstrap-board-and-router.md](references/bootstrap-board-and-router.md),
   which already created `<router-dir>/router.hcl` (a verbatim copy of
   [`examples/router/router.hcl`](../../../examples/router/router.hcl))
   **and** persisted `WORKSPACE_TRELLO_ROUTER_DIR` for them. Skip
   straight to picking `TRELLO_PLAYBOOKS_DIR` (next bullet) and on to
   §6 for the remaining env vars.

2. **Manual path (operator already has a board).** Pick any directory
   the launching user can read, copy
   [`examples/router/router.hcl`](../../../examples/router/router.hcl)
   into it as `router.hcl`, and edit each `name = "…"` in the
   `kanban {}` block so it matches the **exact** Trello list names on
   the existing board. Renaming a list later only requires editing
   this one file — every playbook reads `{{kanban.<role>.name}}` at
   render time. Then export `WORKSPACE_TRELLO_ROUTER_DIR` to the
   absolute path of that directory (§6 has the OS-specific recipes).

For `TRELLO_PLAYBOOKS_DIR`, two reasonable choices:

- **Use this repo's [`playbook/`](../../../playbook/) directory** for
  the in-repo Azure / AVM / Terraform playbooks; clone the repo and
  point `--playbooks-dir` at the absolute path of `playbook/`.
- **Roll a private directory** populated with the operator's own
  `.md` files. The renderer is strict: any `{{kanban.*}}` key not
  declared in `router.hcl` is a startup-fatal `unknown_kanban_key`
  error; missing `{{<basename>}}` cross-references are also fatal.

After both directories exist and the env vars point at them, fall
through to §6 to confirm every required env var is in the shell that
will launch `jjc`.

## 6. Environment variables

Once the user has the four Trello values from §2 in hand (and, later,
the rest from §3 / §5), they must be exported into **the exact same
shell that will launch `jjc`**. JJC reads them at startup; a value
typed into a different terminal or saved into a different shell
profile won't be visible.

Pick the snippet that matches the operator's OS / shell. Two rules
apply to all of them:

1. **Per-session first, persisted later.** Always get a successful
   `jjc` boot from a one-off session export before writing the values
   into any profile / `.env` / supervisor unit. That way a typo only
   wastes one shell, not every future one.
2. **Never paste secrets into a shared chat or commit them.** Treat
   `TRELLO_API_KEY`, `TRELLO_API_TOKEN`, and `TRELLO_API_SECRET` like
   passwords — JJC redacts them to length-fingerprints in its own
   logs for the same reason.

### Windows (PowerShell, per-session)

```powershell
$env:TRELLO_API_KEY            = '<paste-key>'
$env:TRELLO_API_TOKEN          = '<paste-token>'
$env:TRELLO_API_SECRET         = '<paste-oauth-secret>'
$env:TRELLO_KANBAN_BOARD_ID    = '<24-hex-board-id>'
```

`$env:NAME = '…'` only sets the variable for the **current** PowerShell
window. Closing or opening a new tab clears it.

### Windows (PowerShell, persisted for the current user)

```powershell
[Environment]::SetEnvironmentVariable('TRELLO_API_KEY',         '<key>',         'User')
[Environment]::SetEnvironmentVariable('TRELLO_API_TOKEN',       '<token>',       'User')
[Environment]::SetEnvironmentVariable('TRELLO_API_SECRET',      '<secret>',      'User')
[Environment]::SetEnvironmentVariable('TRELLO_KANBAN_BOARD_ID', '<board-id>',    'User')
```

- Scope `'User'` writes to the per-user registry hive; no admin needed
  and other accounts on the box can't read it.
- Already-open shells **do not** pick up the change — open a fresh
  PowerShell window before re-running detection.
- GUI equivalent: *System Properties → Environment Variables → User
  variables → New*.

### Windows (cmd.exe)

Per-session: `set NAME=value` (no quotes around the value, no spaces
around `=`). Persisted per user: `setx NAME "value"` — `setx` writes
to the user hive but, like the scripted variant above, does **not**
affect the current `cmd.exe`; open a new one to use the value.

### macOS / Linux (bash / zsh, per-session)

```bash
export TRELLO_API_KEY='<paste-key>'
export TRELLO_API_TOKEN='<paste-token>'
export TRELLO_API_SECRET='<paste-oauth-secret>'
export TRELLO_KANBAN_BOARD_ID='<24-hex-board-id>'
```

Single quotes prevent the shell from interpreting `$`, `!` and other
metacharacters that occasionally appear in Trello tokens.

### macOS / Linux (persisted)

Two reasonable options — pick **one**, not both:

- **Shell profile.** Append the same four `export …` lines to
  `~/.bashrc` (bash) or `~/.zshrc` (zsh on modern macOS), then reload
  with `source ~/.bashrc` (or open a new terminal). Visible to every
  shell you start as that user.
- **Per-project `.env` + `direnv` (recommended).** Install `direnv`
  (`brew install direnv`, `apt install direnv`, …), add the four
  `export …` lines to `<project-dir>/.envrc`, run
  `direnv allow <project-dir>`, and the values are loaded only when
  you `cd` into that directory. Don't commit `.envrc` — add it to
  `.gitignore`.

### Verify the export worked

Re-run the detection snippet from §2 in a **fresh shell** (or after
sourcing the profile). All four should report `set (len=…)` with
non-zero lengths. If any still show `MISSING`, the export landed in a
different shell / scope than the one you just probed — repeat the
right OS-specific recipe above.

> The remaining required values (`COPILOT_MODEL`,
> `WORKSPACE_TRELLO_ROUTER_DIR`, `TRELLO_PLAYBOOKS_DIR`, and optional
> `LISTEN_ADDR` / `CALLBACK_URL` / `TRELLO_GATEWAY_TUNNEL`) export the
> same way; §3, §5 and §7 will plug them into the same recipes as
> their values become known.

## 7. First run

TBD — recipes for:
- default quick-tunnel start (`--tunnel=cloudflared`);
- production `--tunnel=none` + `--callback-url`;
- how to confirm Trello webhook reconciliation;
- expected `event=*` log lines for a healthy boot.

## 8. Verification and troubleshooting

TBD — symptom → cause → fix table, including:
- `copilot_model_not_available` — model name drift between accounts;
- `signature_invalid` — callback URL mismatch;
- `cloudflared not found` — install path / `--tunnel=none` fallback;
- `playbooks_dir … missing` / `unknown_kanban_key` — template errors;
- `worker_session_create_failed` after the AzureRM refresh hook.

---

## Bundled resources

To be added as the skill grows:

- `references/` — deep-dive notes too long for SKILL.md (per-OS install
  recipes, full env-var matrix, troubleshooting catalog).
- `scripts/` — copy-pasteable / executable helpers (e.g. a
  `verify-setup.ps1` / `verify-setup.sh` that probes every prerequisite
  and prints a checklist).
- `assets/` — sample `router.hcl`, sample `.env`, sample systemd /
  Windows-service unit, etc.

Keep this SKILL.md under ~500 lines. When a section grows past a screen
or two of detail, move the body into `references/<topic>.md` and leave a
one-paragraph summary + pointer here.
