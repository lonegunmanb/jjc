# `examples/router/` — sample playbooks + router configuration

This directory holds **two layers** of sample configuration. They are at
different stages of completeness:

| Layer | What it covers | Status |
|---|---|---|
| `--config-src` (this layer) | Loading and pre-rendering the per-card playbook `.md` files that live alongside `router.hcl` in the same directory. | **Implemented** (see `internal/app/prompttmpl/`). |
| HCL router (`router.hcl`) | `kanban {}` shape, `route {}` event routing, `rule {}` playbook selection, `github_issue` HCL function. | **Implemented** (see `internal/app/kanban/` and `internal/app/router/`). |

Files in this folder:

- [router.hcl](./router.hcl) — sample HCL config (kanban / route /
  rule / `github_issue`) loaded by the gateway from `<config-src>/router.hcl`.

The actual in-repo collection of playbook `.md` files lives **outside this
folder**, at the workspace root: [../../playbook/](../../playbook/). Point
`--config-src` at a directory that holds both `router.hcl` and those
`.md` files (or at a hashicorp/go-getter v2 source that publishes the
same layout) to use them.

---

## What the gateway does today (the playbooks layer)

At startup the gateway:

1. Resolves `--config-src` (precedence: flag > `JJC_CONFIG_SRC` env;
   required, no default). If the value is a local path it must exist
   and be a directory — the bundle ships `router.hcl` plus every
   playbook `.md` at the top level. If the value is a remote
   hashicorp/go-getter v2 source (git, https, github shortcut, ...)
   JJC downloads it into a per-process temp directory at startup and
   removes that directory on shutdown.
2. Creates a per-process temp directory via
   `os.MkdirTemp("", "jjc-playbooks-*")` and writes the path to the
   log as `event=playbooks_tempdir_created path=...`.
3. Materialises five **embedded skeleton prompts** into the temp dir as
   defaults: `BOOTSTRAP.md`, `IDENTITY.md`, `WORKER.md`, `TOOLS.md`,
   `USER.md` (these ship inside the binary, baked from
   [../../internal/app/prompts/](../../internal/app/prompts/)).
4. Copies every `.md` file under `<config-src>` on top — any user file
   with the same basename **overrides** the embedded skeleton.
5. For every file in the temp dir, substitutes each `{{<basename>}}`
   reference with the absolute path of `<basename>` inside the temp dir.
   Any reference whose target is missing or whose name contains `/`,
   `\`, `..`, or is empty causes the gateway to log
   `event=playbook_render_failed file=... line=... column=... reference=... reason=...`
   and exit non-zero.
6. Uses the rendered files to assemble each per-card worker system
   prompt. The temp dir is removed on process exit.

### Cross-playbook reference syntax

Inside any `.md` file, refer to another playbook by its bare basename
wrapped in `{{ }}`:

```markdown
…see [Plan §8]({{azurerm_provider_issue_bug_plan.md}}) for the full
checklist…
```

After rendering the worker sees something like:

```markdown
…see [Plan §8](C:\Users\you\AppData\Local\Temp\jjc-playbooks-1234567890\azurerm_provider_issue_bug_plan.md) for the full
checklist…
```

Rules for the `{{...}}` body:

- Bare basename only: no `/`, no `\`, no `..`, must not be empty.
- Must match a `.md` file present either in `<config-src>` or in the
  embedded defaults. Mismatches fail at startup with line/column info.
- Whitespace inside the braces is trimmed: `{{ foo.md }}` is the same
  as `{{foo.md}}`.

### What you do NOT need to wrap in `{{...}}`

Leave these alone — they have nothing to do with the playbooks temp dir:

- External URLs (`https://github.com/...`).
- Files that live inside the worker's `work_dir` (the cloned repo), e.g.
  `go.mod`, `README.md`, `.github/instructions/*.instructions.md`.
- Runtime files managed by the gateway itself, such as per-worker activity logs.
- Glob patterns or pure narrative mentions of file names.

> **Deprecation note: `<config-src>/scripts/refresh-copilot-setup.ps1` is no
> longer required.** The AzureRM provider work_dir hook used to spawn an
> ephemeral Copilot session whose only job was to invoke that script via
> `pwsh -NoProfile -File ...`. As of [#13](https://github.com/lonegunmanb/trello-copilot/issues/13)
> the gateway ships an in-process `internal/app/aiassistedrefresh`
> package that clones
> `WodansSon/terraform-azurerm-ai-assisted-development` and runs the
> upstream installer directly: `pwsh + install-copilot-setup.ps1` on
> Windows, `bash + install-copilot-setup.sh` on macOS / Linux. The
> refresh is synchronous (no LLM turn, no spawned session) and works on
> any OS where `git` is on `$PATH` and either `pwsh` or `bash` is
> available. Legacy helper scripts under `<config-src>/scripts/` are no
> longer invoked by the gateway.

---

## Suggested directory layout

The gateway's flat-file convention:

```
<--config-src>/                   # required, no default
├── router.hcl                     # required: kanban {} + route {} + rule {} blocks
├── BOOTSTRAP.md                  # optional override of embedded default
├── IDENTITY.md                   # optional override of embedded default
├── WORKER.md                     # optional override of embedded default
├── TOOLS.md                      # optional override of embedded default
├── USER.md                       # optional override of embedded default
├── azurerm_provider_issue.md
├── azurerm_provider_issue_classification.md
├── azurerm_provider_issue_bug.md
├── azurerm_provider_issue_bug_plan.md
├── azurerm_provider_issue_bug_action.md
├── azurerm_provider_issue_feature_request.md
├── azurerm_provider_issue_question.md
├── azurerm_provider_issue_security.md
├── azurerm_provider_pr.md
├── azurerm_provider_pr_naming_guidance.md
├── avm_issue.md
├── avm_issue_bug.md
├── avm_issue_feature_request.md
├── avm_issue_question.md
├── avm_issue_security.md
├── avm_pr.md
├── avm_pr_plan.md
├── avm_pr_action.md
├── tfvm_issue.md
├── tfvm_issue_bug.md
├── tfvm_issue_feature_request.md
├── tfvm_issue_question.md
├── tfvm_issue_security.md
└── tfvm_pr.md
```

Subdirectories are ignored. Only `.md` files are loaded.

To use the playbooks shipped in this repo, run:

```powershell
jjc.exe --config-src .\playbook
# or
$env:JJC_CONFIG_SRC = "C:\path\to\config-src"
jjc.exe
```

---

## The HCL router

[router.hcl](./router.hcl) shows the shape of `<config-src>/router.hcl`.
It introduces three blocks:

1. `kanban {}` — names the Trello lists by **role** (plan, action,
   wait.\*, done) so prompts can talk about roles instead of column
   names.
2. `route {}` — decides whether each Trello webhook event should be dropped,
   dispatched, sent as a departure notice, or treated as a terminate
   signal.
3. `rule {}` — given a matched card, picks which playbook(s) to feed to the worker.
   Playbook names are bare basenames (same convention as `{{...}}`
   above) and the engine resolves them through the `<config-src>`
   renderer described in the previous section.

### Tracking issues

- `lonegunmanb/trello-copilot#5` — `kanban {}` shape + go-trello-sdk
  bootstrap.
- `lonegunmanb/trello-copilot#6` — `route {}` engine.
- `lonegunmanb/trello-copilot#7` — `rule {}` engine + `github_issue`
  HCL function.
- `lonegunmanb/trello-copilot#1` — migrate `AzureRMRefreshHook`'s
  ad-hoc system prompt onto the same playbooks renderer (uses #7's
  structured input).
