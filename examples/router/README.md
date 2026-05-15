# `examples/router/` — sample playbooks + router configuration

This directory holds **two layers** of sample configuration. They are at
different stages of completeness:

| Layer | What it covers | Status |
|---|---|---|
| `--playbooks-dir` (this layer) | Loading and pre-rendering the per-card playbook `.md` files. | **Implemented** (see `internal/app/prompttmpl/`). |
| HCL router (`router.hcl`) | `kanban {}` shape, `route {}` event routing, `rule {}` playbook selection, `github_issue` HCL function. | **Proposal** (not wired in yet). Today the gateway uses `internal/app/routing.go` + `internal/app/worktype.go` for these decisions. |

Files in this folder:

- [router.hcl](./router.hcl) — full HCL spec proposal (kanban / route /
  rule / `github_issue`). Not loaded by the running gateway today; it
  documents the target shape so reviewers can read what the routing
  layer is supposed to look like once issues #5 / #6 / #7 land.

The actual in-repo collection of playbook `.md` files lives **outside this
folder**, at the workspace root: [../../playbook/](../../playbook/). Point
`--playbooks-dir` at that directory to use them.

---

## What the gateway does today (the playbooks layer)

At startup the gateway:

1. Resolves `--playbooks-dir` (precedence: flag > `TRELLO_PLAYBOOKS_DIR`
   env > default `<cwd>/.playbooks`). The directory must exist; otherwise
   the gateway logs `event=playbooks_dir_invalid` and exits non-zero.
2. Creates a per-process temp directory via
   `os.MkdirTemp("", "openclaw-playbooks-*")` and writes the path to the
   log as `event=playbooks_tempdir_created path=...`.
3. Materialises five **embedded skeleton prompts** into the temp dir as
   defaults: `BOOTSTRAP.md`, `IDENTITY.md`, `WORKER.md`, `TOOLS.md`,
   `USER.md` (these ship inside the binary, baked from
   [../../internal/app/prompts/](../../internal/app/prompts/)).
4. Copies every `.md` file under `--playbooks-dir` on top — any user file
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
…see [Plan §8](C:\Users\you\AppData\Local\Temp\openclaw-playbooks-1234567890\azurerm_provider_issue_bug_plan.md) for the full
checklist…
```

Rules for the `{{...}}` body:

- Bare basename only: no `/`, no `\`, no `..`, must not be empty.
- Must match a `.md` file present either in `--playbooks-dir` or in the
  embedded defaults. Mismatches fail at startup with line/column info.
- Whitespace inside the braces is trimmed: `{{ foo.md }}` is the same
  as `{{foo.md}}`.

### What you do NOT need to wrap in `{{...}}`

Leave these alone — they have nothing to do with the playbooks temp dir:

- External URLs (`https://github.com/...`).
- Files that live inside the worker's `work_dir` (the cloned repo), e.g.
  `go.mod`, `README.md`, `.github/instructions/*.instructions.md`.
- Helper scripts (`refresh-copilot-setup.ps1`,
  `scripts/trello-get-card-info.ps1`).
- Glob patterns or pure narrative mentions of file names.

---

## Suggested directory layout

The gateway's flat-file convention:

```
<--playbooks-dir>/                # default <cwd>/.playbooks
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
trello-openclaw-webhook-gateway.exe --playbooks-dir .\playbook
# or
$env:TRELLO_PLAYBOOKS_DIR = "C:\path\to\playbook"
trello-openclaw-webhook-gateway.exe
```

---

## The HCL router (proposal — not implemented)

[router.hcl](./router.hcl) shows what `--router-dir/router.hcl` would
look like once the HCL-based router is implemented. It introduces three
blocks:

1. `kanban {}` — names the Trello lists by **role** (plan, action,
   wait.\*, done) so prompts can talk about roles instead of column
   names.
2. `route {}` — replaces today's hard-coded
   [../../internal/app/routing.go](../../internal/app/routing.go);
   decides whether each Trello webhook event should be dropped,
   dispatched, sent as a departure notice, or treated as a terminate
   signal.
3. `rule {}` — replaces today's hard-coded
   [../../internal/app/worktype.go](../../internal/app/worktype.go);
   given a matched card, picks which playbook(s) to feed to the worker.
   Playbook names are bare basenames (same convention as `{{...}}`
   above) and the engine resolves them through the `--playbooks-dir`
   renderer described in the previous section.

Until the HCL router lands, the playbook chosen for each card is still
decided by `EntryPlaybookFilename` in
[../../internal/app/worktype.go](../../internal/app/worktype.go).

### Tracking issues

- `lonegunmanb/trello-copilot#5` — `kanban {}` shape + go-trello-sdk
  bootstrap.
- `lonegunmanb/trello-copilot#6` — `route {}` engine.
- `lonegunmanb/trello-copilot#7` — `rule {}` engine + `github_issue`
  HCL function.
- `lonegunmanb/trello-copilot#1` — migrate `AzureRMRefreshHook`'s
  ad-hoc system prompt onto the same playbooks renderer (uses #7's
  structured input).
