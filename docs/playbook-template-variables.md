# Playbook Template Variables — Authoring Specification

> **Audience.** Anyone (human or autonomous agent) editing a `.md` file
> under `--playbooks-dir` (typically [playbook/](../playbook/)) or under
> the in-binary embedded defaults at [internal/app/prompts/](../internal/app/prompts/).
> Read this document end-to-end **before** touching a playbook that contains
> Trello list names, list IDs, or the literal `[agent]:` comment prefix.
>
> **Scope.** This document defines the **complete schema** of template
> variables the gateway substitutes into playbook `.md` files at process
> startup. It is the only authoritative reference; if a variable is not
> listed here, it does not exist and using it in a playbook will fail
> startup. Likewise, any `router.hcl`-derived value that appears
> hard-coded in a playbook is a defect — every such occurrence must be
> rewritten using the variables defined here.
>
> **Why this exists.** Trello list names, list IDs, and the
> agent-comment prefix are **operator-configurable** (see
> [examples/router/router.hcl](../examples/router/router.hcl)). Hard-coding
> them in a playbook means a single rename in `router.hcl` silently
> desynchronises the prompt from the routing layer; that defect is the
> root cause of issue [#30](https://github.com/lonegunmanb/trello-copilot/issues/30).
> Templating those values eliminates the class entirely: after startup,
> what the worker reads in the prompt is what the gateway actually uses
> to route — by construction.

---

## 1. Two kinds of `{{...}}` references coexist in a playbook

Every playbook may contain references of either of two shapes inside
double-brace markers `{{` … `}}`. They share the same syntactic
container but mean different things and are resolved by different
rules. The renderer picks which kind a given reference is by inspecting
the trimmed body inside the braces:

| Kind | Body shape | Resolved to | Owner |
|---|---|---|---|
| **Cross-playbook reference** (existing) | A bare basename ending in `.md`, e.g. `azurerm_provider_issue_bug.md`. No `/`, no `\`, no `..`, must not be empty. | The absolute path of that playbook in the per-process temp directory. | `internal/app/prompttmpl/` (already implemented). |
| **Kanban variable** (new) | A dotted key starting with `kanban.`, e.g. `kanban.action.id`. | A string drawn from the resolved `kanban {}` view. The substitution is the literal value — no quoting, no escaping. | Same package, additional pass (specified below). |

Disambiguation is unambiguous:

- Body matches `^kanban\.` → **kanban variable**. Validated against the
  schema in §2; any unknown key fails startup.
- Otherwise → **cross-playbook reference**. Validated by the existing
  basename rules.

> Whitespace inside braces is trimmed: `{{ kanban.action.id }}` ≡
> `{{kanban.action.id}}`.

A single playbook may use both kinds freely; their order on a line does
not matter.

---

## 2. Schema — complete enumeration of supported keys

The table below lists **every** key the renderer accepts under the
`kanban.` namespace. Anything else with that prefix is rejected at
startup with `event=playbook_render_failed reason=unknown_kanban_key`.

### 2.1 Identity

| Key | Type | Value at runtime |
|---|---|---|
| `kanban.board.id` | string | The Trello board ID, i.e. the value passed to `--kanban-board-id`. Always non-empty. |

### 2.2 Per-role values (seven roles)

For each of the **seven** roles declared in the `kanban {}` block —
`plan`, `action`, `done`, `wait.plan_review`, `wait.action_review`,
`wait.generic`, `wait.exception` — the renderer exposes two keys:

| Key shape | Type | Value |
|---|---|---|
| `kanban.<role>.id` | string | The resolved Trello list ID for that role. Always non-empty. |
| `kanban.<role>.name` | string | The verbatim list name that resolution matched against, taken from `router.hcl`'s `name = "..."` field (trimmed but not lower-cased). Use this **only** when human-readable narrative is wanted; do not anchor matching rules on it. |

`<role>` is one of the seven role keys, written with literal dots for
the wait sub-roles:

| Role token | Default list name (router.hcl) | `.id` key | `.name` key |
|---|---|---|---|
| `plan` | `Analyze` | `kanban.plan.id` | `kanban.plan.name` |
| `action` | `In action` | `kanban.action.id` | `kanban.action.name` |
| `wait.plan_review` | `Ready for plan review` | `kanban.wait.plan_review.id` | `kanban.wait.plan_review.name` |
| `wait.action_review` | `Ready for review` | `kanban.wait.action_review.id` | `kanban.wait.action_review.name` |
| `wait.generic` | `Pending PR` | `kanban.wait.generic.id` | `kanban.wait.generic.name` |
| `wait.exception` | `Need Attention` | `kanban.wait.exception.id` | `kanban.wait.exception.name` |
| `done` | `Done` | `kanban.done.id` | `kanban.done.name` |

> The seven role tokens are **the contract** of the routing layer. They
> are stable across renames of the Trello columns; `router.hcl` is the
> only file that maps a column name to a role.

### 2.3 Category aggregates

The HCL `route {}` engine groups roles into four **categories**.
Playbooks frequently need to say "any wait list" in human-readable
narrative; the aggregates expose those category-level lists as a single
comma-and-space-joined string (deterministic, sorted) so a prompt can
read naturally:

| Key | Type | Value |
|---|---|---|
| `kanban.plan.list_ids` | string | Every list ID in the `plan` category, joined by `, `. (Today this is exactly `kanban.plan.id`; the key exists for symmetry and forward compatibility.) |
| `kanban.action.list_ids` | string | Same idea for the `action` category. |
| `kanban.wait.list_ids` | string | Every list ID in the `wait` category — i.e. all four `wait.*` roles **plus** any list on the board no role claimed (collapsed into wait by the route engine), joined by `, `. |
| `kanban.done.list_ids` | string | Same idea for the `done` category. |

> Joining is purely cosmetic — these strings exist so a sentence like
> *"if the card sits in any wait list ({{kanban.wait.list_ids}}), do
> not modify GitHub"* reads correctly. The LLM is **not** expected to
> split the joined string back into individual IDs at runtime; for
> per-ID matching, use the per-role `.id` keys plus the no-match
> fallback rule (see §3.4).

### 2.4 Agent-comment prefix

`router.hcl`'s `agent_comment_prefixes` is a list of strings the
gateway recognises as agent-authored (used to break the comment
feedback loop). Two keys are exposed:

| Key | Type | Value |
|---|---|---|
| `kanban.agent_comment_prefix` | string | The **first** entry of `agent_comment_prefixes`. This is the prefix workers **must use when writing comments** — every `trello_card_comment` call must begin its `text` with this exact string followed by a space. |
| `kanban.agent_comment_prefixes` | string | All entries of `agent_comment_prefixes`, joined by `, ` in declaration order. Use this only in descriptive narrative ("comments starting with one of {{kanban.agent_comment_prefixes}} are recognised as agent self-comments"). |

> Singular vs plural matters. `kanban.agent_comment_prefix` is the
> **active** prefix the worker must produce; `kanban.agent_comment_prefixes`
> is for **descriptive** prose only. Never write a comment using
> the plural form.

---

## 3. Renderer behaviour

### 3.1 When the substitution runs

Exactly once, at gateway startup, **after** `kanban.LoadAndResolve`
succeeds and **before** any worker session is spawned. The order
inside `internal/app/prompttmpl/` is:

1. Materialise embedded defaults into the per-process temp dir.
2. Overlay every `.md` file from `--playbooks-dir`.
3. For every file in the temp dir, apply the substitution pass:
   - First, the existing `{{<basename>.md>}}` resolution.
   - Then, the new `{{kanban.*}}` resolution.
4. Write the rewritten content back to the same path in the temp dir.
5. From this point on, only the rendered copies are read.

`Renderer.Cleanup()` removes the temp dir at process exit.

### 3.2 Strict mode

The renderer is **strict**. The following all fail startup with
`event=playbook_render_failed file=<basename> line=<n> column=<c>
reference=<raw body> reason=<code>` (the gateway logs the event and
exits non-zero):

| `reason` code | Condition |
|---|---|
| `unknown_kanban_key` | The body starts with `kanban.` but does not match any key in §2. |
| `malformed_reference` | The body is empty after trimming, or contains characters that fit neither a basename nor a `kanban.*` key. |
| `referenced_file_not_found` | A `{{<basename>.md>}}` reference points to a basename not present in the temp dir. (Existing rule.) |
| `rendered_size_exceeded` | The rewritten file exceeds `MaxRenderedFileBytes`. (Existing rule.) |

> There is no "best-effort" fallback. A typo such as
> `{{kanban.plan.di}}` is a startup-time fatal error, not a runtime
> empty string in the worker's system prompt. This is intentional:
> silent fallbacks are exactly the failure mode #30 was filed against.

### 3.3 No nesting, no expressions, no escapes

The substitution is a single linear pass. The renderer does **not**
support:

- Nested references such as `{{kanban.{{role_name}}.id}}`.
- Default values (`{{kanban.plan.id|fallback}}`).
- Conditionals or loops.
- Escapes (a literal `{{` followed by `kanban.…}}` in a playbook is
  always interpreted as a reference — wrap the example in a fenced
  code block if you need to discuss the syntax without it being
  resolved). For unambiguous escape, surround the example with
  backticks; the renderer does not look inside fenced ``` ``` blocks
  for `{{...}}` patterns.

### 3.4 The no-role-match rule (still authored at the WORKER level)

The renderer guarantees the seven role IDs exist; it does **not**
encode the rule for what to do when a card sits in an unclaimed list.
That rule lives in WORKER.md and is expressed in terms of the
templated values:

> When the result of `trello_card_list().id` matches none of
> `kanban.plan.id`, `kanban.action.id`, `kanban.wait.plan_review.id`,
> `kanban.wait.action_review.id`, `kanban.wait.generic.id`,
> `kanban.wait.exception.id`, `kanban.done.id`, treat the card as
> belonging to the `wait.generic` role …

(Worded this way, the rule survives any future change to the role
taxonomy because the variables it cites do not move.)

---

## 4. What **NOT** to template

Some values look templatable but are intentionally **not** part of this
schema, because they are per-card or per-session and the gateway
injects them through `# CARD CONTEXT` at session spawn time, not at
playbook render time. Do not invent new `{{...}}` references for them.

| Value | Where it comes from |
|---|---|
| `card_id` | CARD CONTEXT field, set per session. |
| `work_dir` | CARD CONTEXT field, set per session. |
| `work_type`, `kind` | CARD CONTEXT field, set per session. |
| `github_repo`, `github_number`, `github_url` | CARD CONTEXT field, set per session. |
| Per-card list ID (the list the card currently sits in) | Comes from `trello_card_list().id` at runtime; the worker compares it against the templated role IDs. |
| External URLs | Plain text; do not wrap. |
| Files inside the worker's `work_dir` (`go.mod`, `README.md`, …) | Plain text; do not wrap. |

> Rule of thumb: if a value is fixed at gateway startup and applies to
> every card the gateway might process, it is a candidate for
> templating here. If it varies per card, it belongs in CARD CONTEXT,
> not in this schema.

---

## 5. Authoring guide — how to convert a playbook

### 5.1 The mechanical rule

For every occurrence of a Trello list name or the literal `[agent]:`
prefix in a playbook, replace it with the corresponding key from §2:

| You see | Replace with | Notes |
|---|---|---|
| `Analyze` (as a Trello list reference) | `{{kanban.plan.name}}` | Use `.name` for narrative; use `.id` when referring to the identifier the worker compares against. |
| `In action` (Trello list) | `{{kanban.action.name}}` | |
| `Ready for plan review` | `{{kanban.wait.plan_review.name}}` | |
| `Ready for review` | `{{kanban.wait.action_review.name}}` | |
| `Pending PR` | `{{kanban.wait.generic.name}}` | |
| `Need Attention` | `{{kanban.wait.exception.name}}` | |
| `Done` (Trello list) | `{{kanban.done.name}}` | |
| `[agent]:` (when telling the worker how to write a comment) | `{{kanban.agent_comment_prefix}}` | Active prefix the worker must produce. |
| Narrative phrases like "comments starting with `[agent]:`" | `{{kanban.agent_comment_prefixes}}` | Descriptive only. |
| "Move the card to the `Ready for plan review` list" | "Move the card to `{{kanban.wait.plan_review.name}}` (list ID `{{kanban.wait.plan_review.id}}`)" | Templating both gives the worker both the human-readable name (for log lines) and the stable ID (for `target_list_id`). |
| "If the card is in any wait column" | "If the card sits in any wait list (`{{kanban.wait.list_ids}}`)" | Use the category aggregate. |
| The word "Done" used as English ("you are done") | Leave it. | Only template when the word is referring to the **Trello list**. |

### 5.2 Worked example: a rule line

**Before** (PR-1 backstop state, anchors on a hard-coded English name):

```markdown
- **⚠️ 卡片不在 In action 列时，禁止任何变更 GitHub 的操作**：…
```

**After**:

```markdown
- **⚠️ 卡片不在 `action` 角色对应的列（默认列名 `{{kanban.action.name}}`，
  list ID `{{kanban.action.id}}`）时，禁止任何变更 GitHub 的操作**：…
```

The role token `action` is the **stable anchor** for the rule's meaning;
`{{kanban.action.name}}` and `{{kanban.action.id}}` are concrete facts
the worker can use without invention.

### 5.3 Worked example: a worker comment instruction

**Before**:

```markdown
评论一句 `[agent]: 卡片已移到 Need Attention，已交接并清理完毕，等待人工介入。`
```

**After**:

```markdown
评论一句 `{{kanban.agent_comment_prefix}} 卡片已移到 {{kanban.wait.exception.name}}，
已交接并清理完毕，等待人工介入。`
```

After rendering, this becomes (with default config):

```markdown
评论一句 `[agent]: 卡片已移到 Need Attention，已交接并清理完毕，等待人工介入。`
```

Identical to the original — but now if the operator renames the list
to `要人盯` and changes the prefix to `[claw]:`, the same playbook line
becomes:

```markdown
评论一句 `[claw]: 卡片已移到 要人盯，已交接并清理完毕，等待人工介入。`
```

…with no edit to the playbook.

### 5.4 Worked example: a table row keyed by role

**Before** (anchors are list names; rule lookup is implicit
string-matching):

```markdown
| 类别 | 包含 list | 期待终态 | 你该做的事 |
|------|-----------|----------|-----------|
| **推进类** | Analyze、In action | 把卡片向前推一格 | … |
```

**After** (anchors are role tokens; list names appear only as a
parenthetical reading aid, automatically kept in sync via the
template):

```markdown
| 类别 | 包含角色（默认列名） | 期待终态 | 你该做的事 |
|------|----------------------|----------|-----------|
| **推进类** | `plan`（{{kanban.plan.name}}）、`action`（{{kanban.action.name}}） | 把卡片向前推一格 | … |
```

### 5.5 Patterns to actively avoid

1. **Do not paste a raw English list name "for clarity"** next to a
   templated key. If you want both the role token and the list name,
   use the template — never both `{{kanban.action.name}}` and the
   literal `In action`. A mismatch between the two during a future
   rename is exactly the failure mode this schema eliminates.
2. **Do not invent new `kanban.*` keys** in a playbook. If you find
   yourself wanting one, file an issue describing the use case and
   propose an addition to §2 first.
3. **Do not template per-card values** (see §4) — they come from CARD
   CONTEXT, not from this renderer.
4. **Do not put a `{{kanban.*}}` reference inside the body of another
   `{{...}}` reference**. Nesting is explicitly unsupported (§3.3).
5. **Do not template the Trello tool names** (`trello_card_list`,
   `trello_card_move`, …). They are part of the gateway's
   in-process tool registry, not configurable.

### 5.6 Self-check before committing a playbook change

Run through this checklist on the diff:

- [ ] Every Trello list name appearing in the new content uses
      `{{kanban.<role>.name}}` (or `.id` where an ID is meant).
- [ ] No literal `[agent]:` survives in any worker-authored example;
      every occurrence uses `{{kanban.agent_comment_prefix}}` or
      (for descriptive prose only) `{{kanban.agent_comment_prefixes}}`.
- [ ] Every templated key is in the §2 schema. Typos will fail
      startup; catching them in review is faster.
- [ ] Existing `{{<basename>.md>}}` cross-references still resolve
      (re-run `go test ./internal/app/prompttmpl/...` after editing).
- [ ] You did not template anything in §4's "do not template" list.

---

## 6. Pointer for implementers

The renderer extension itself is tracked separately (planned PR 2a of
issue #30). When implementing:

- The single source of truth for which keys exist is §2 of **this**
  document. The renderer should derive its key set programmatically
  from `kanban.Resolved` so the spec and the implementation cannot
  drift; the spec then documents the contract for authors.
- Resolution proceeds in two passes: cross-playbook references first
  (existing), then `kanban.*` keys. Either may fail with
  `*RenderError`; both populate the same `file:line:column reference`
  fields so the existing log format keeps working.
- `kanban.Resolved` already exposes everything the schema needs.
  `BoardID` → `kanban.board.id`; the `Plan`/`Action`/`Done`/`Wait.*`
  `Role` structs supply `.id`/`.name`; the `*ListIDs` slices feed the
  category aggregates; `AgentCommentPrefixes[0]` (after a
  non-empty-list invariant check) supplies the singular prefix.
- Update [examples/router/README.md](../examples/router/README.md) to
  cross-link to this spec from the playbook layer section.
- Update [internal/app/prompttmpl/embed_test.go](../internal/app/prompts/embed_test.go)
  to assert that the embedded `WORKER.md` (after the role-anchored
  rewrite in PR 2b) contains no occurrence of the seven default list
  names or the literal `[agent]:` prefix outside of fenced code blocks
  that document this schema.

---

## 7. Versioning

This document is the contract between playbook authors and the
renderer. Breaking changes — removing a key, changing the joining
character of an aggregate, switching `kanban.agent_comment_prefix` from
"first entry" to something else — must be paired with a CHANGELOG entry
and a migration note in this section. Additive changes (new keys) are
non-breaking and require only a row added to §2.

Initial revision: this document is created alongside issue #30's PR 2a
(renderer extension); the first wave of playbook rewrites that consume
the new keys lands in PR 2b. Until PR 2a ships, the keys in §2 do
**not** yet resolve — playbooks must not use them yet. After PR 2a
ships, the §1 backstop paragraph at the top of WORKER.md becomes
redundant and is removed by PR 2b.
