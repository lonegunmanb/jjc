# Bootstrap a fresh Trello board + `router-dir`

Use this reference when the user **does not yet have a Trello board**
for JJC to drive (typically a brand-new Trello account, or an existing
account that has never been used for kanban work). It also doubles as
the easiest way to land a working `--router-dir/router.hcl` on disk —
the bootstrap script writes one alongside the new board id.

What the procedure does, end to end:

1. Ask the user for a board name (just one question).
2. Use the Trello REST API to create the board with `defaultLists=false`
   (skip Trello's default *To Do / Doing / Done*).
3. Create the seven JJC-shaped lists, left to right:
   `Need Attention`, `Pending PR`, `Analyze`, `Ready for plan review`,
   `In action`, `Ready for review`, `Done`.
4. Create a `router-dir/` directory at a user-chosen path and drop the
   stock `router.hcl` from `examples/router/router.hcl` into it.
5. Persist `TRELLO_KANBAN_BOARD_ID` (new board id) and
   `WORKSPACE_TRELLO_ROUTER_DIR` (absolute path of the directory from
   step 4) at the user / shell-profile scope, **and** export them into
   the current shell so the rest of [SKILL.md §6](../SKILL.md) still
   sees them.
6. Tell the user how to pick the new persisted env vars up
   (new PowerShell window on Windows; `source` or new terminal on
   *nix), then how to start `jjc` pointing at `--playbooks-dir`.

> **Ask the user one thing at a time.** This procedure has two free-text
> answers (board name, router-dir path). Ask them in two separate turns;
> do not bundle them. Same reason as the
> [credentials walkthrough](trello-credentials.md): combined prompts
> come back partial or swapped.

---

## 1. Prerequisites

Before running anything in this reference:

- `TRELLO_API_KEY` and `TRELLO_API_TOKEN` must already be exported in
  the current shell (the API calls below authenticate with them). If
  the detection snippet from
  [trello-credentials.md §1](trello-credentials.md) shows either as
  `MISSING`, complete
  [trello-credentials.md §4](trello-credentials.md) first.
- `TRELLO_API_SECRET` is **not** needed here — webhook signing only
  matters once `jjc` is running. The bootstrap touches REST only.
- Outbound HTTPS to `api.trello.com` and
  `raw.githubusercontent.com` must work (the bootstrap fetches the
  reference `router.hcl` from the JJC repository).

A quick sanity probe to confirm key + token actually authenticate
before creating anything (see also
[trello-credentials.md §7](trello-credentials.md)):

```powershell
Invoke-RestMethod "https://api.trello.com/1/members/me?key=$env:TRELLO_API_KEY&token=$env:TRELLO_API_TOKEN" |
    Select-Object id, username
```

```bash
curl -fsS "https://api.trello.com/1/members/me?key=$TRELLO_API_KEY&token=$TRELLO_API_TOKEN" | jq '{id, username}'
```

A `200 OK` with the user's identity means it is safe to proceed.

---

## 2. Ask the user for a board name

One question, one answer:

> What should the new Trello board be called? (e.g. `JJC Operations`,
> `terraform-azurerm triage`, …) The name is a free-text label —
> nothing in `router.hcl` keys off it, so pick anything that makes
> sense to the operator.

Store the answer as `$boardName` / `$BOARD_NAME` below.

---

## 3. Create the board and the seven lists

### PowerShell (Windows)

```powershell
$boardName = '<answer from §2>'
$lists     = 'Need Attention','Pending PR','Analyze','Ready for plan review','In action','Ready for review','Done'

# 3a. Create the board with no default lists.
$encoded = [uri]::EscapeDataString($boardName)
$board   = Invoke-RestMethod -Method Post -Uri (
    "https://api.trello.com/1/boards/?name=$encoded&defaultLists=false" +
    "&key=$env:TRELLO_API_KEY&token=$env:TRELLO_API_TOKEN")
$boardId = $board.id
"Board id: $boardId"
"Board URL: $($board.url)"

# 3b. Append each list in left-to-right order. pos=bottom puts each
#     new list at the right end, so the on-board order matches $lists.
foreach ($name in $lists) {
    $encoded = [uri]::EscapeDataString($name)
    Invoke-RestMethod -Method Post -Uri (
        "https://api.trello.com/1/lists?name=$encoded&idBoard=$boardId&pos=bottom" +
        "&key=$env:TRELLO_API_KEY&token=$env:TRELLO_API_TOKEN") | Out-Null
    "Created list: $name"
}
```

### bash / zsh (macOS, Linux)

```bash
BOARD_NAME='<answer from §2>'
LISTS=('Need Attention' 'Pending PR' 'Analyze' 'Ready for plan review' 'In action' 'Ready for review' 'Done')

# 3a. Create the board with no default lists.
encoded=$(jq -rn --arg n "$BOARD_NAME" '$n|@uri')
BOARD_JSON=$(curl -fsS -X POST \
    "https://api.trello.com/1/boards/?name=$encoded&defaultLists=false&key=$TRELLO_API_KEY&token=$TRELLO_API_TOKEN")
BOARD_ID=$(printf '%s' "$BOARD_JSON" | jq -r .id)
BOARD_URL=$(printf '%s' "$BOARD_JSON" | jq -r .url)
echo "Board id:  $BOARD_ID"
echo "Board URL: $BOARD_URL"

# 3b. Append each list in left-to-right order.
for name in "${LISTS[@]}"; do
    encoded=$(jq -rn --arg n "$name" '$n|@uri')
    curl -fsS -X POST \
        "https://api.trello.com/1/lists?name=$encoded&idBoard=$BOARD_ID&pos=bottom&key=$TRELLO_API_KEY&token=$TRELLO_API_TOKEN" \
        >/dev/null
    echo "Created list: $name"
done
```

> **Re-running this script creates a *second* board.** The Trello API
> does not de-duplicate by name. If the user accidentally runs it
> twice, ask which `BOARD_ID` they want to keep and archive the other
> board manually (board settings → *Close board*).

---

## 4. Ask the user for the router-dir path

Second free-text question (after step 3 has reported the board id):

> Where should the JJC router directory live? Press Enter for the
> default:
> - Windows: `%USERPROFILE%\.jjc\router`
> - macOS / Linux: `$HOME/.jjc/router`

Both defaults are safe — JJC only needs the directory to be readable
by the operator and to contain `router.hcl`. Store the answer as
`$routerDir` / `$ROUTER_DIR`.

---

## 5. Materialise `router-dir/router.hcl`

The bootstrap copies the stock router from the JJC repository so the
file always matches the seven-list shape created in §3. The reference
contents live at
<https://raw.githubusercontent.com/lonegunmanb/jjc/main/examples/router/router.hcl>.

### PowerShell

```powershell
$routerDir = '<answer from §4, or default>'
New-Item -ItemType Directory -Force -Path $routerDir | Out-Null
Invoke-WebRequest `
    -Uri 'https://raw.githubusercontent.com/lonegunmanb/jjc/main/examples/router/router.hcl' `
    -OutFile (Join-Path $routerDir 'router.hcl')
"Router file: $(Join-Path $routerDir 'router.hcl')"
```

### bash / zsh

```bash
ROUTER_DIR='<answer from §4, or default>'
mkdir -p "$ROUTER_DIR"
curl -fsSL \
    https://raw.githubusercontent.com/lonegunmanb/jjc/main/examples/router/router.hcl \
    -o "$ROUTER_DIR/router.hcl"
echo "Router file: $ROUTER_DIR/router.hcl"
```

The file is a verbatim copy of [`examples/router/router.hcl`](../../../../examples/router/router.hcl).
Its `kanban {}` block already names exactly the seven lists that step 3
created, so it works without further edits. The operator can customise
`route {}` and `rule {}` later.

> If the host is air-gapped / blocks `raw.githubusercontent.com`, fall
> back to copying `examples/router/router.hcl` from a local checkout of
> `github.com/lonegunmanb/jjc`. Do **not** hand-retype the file — it
> is several hundred lines and a typo in a `when =` expression is a
> startup-fatal error.

---

## 6. Persist the two new env vars + load them now

Both values are needed by `jjc` at every startup, so they must be
persisted, **and** loaded into the current shell so the rest of
[SKILL.md §6](../SKILL.md) and the first `jjc` run can see them
without restarting.

### PowerShell (Windows)

```powershell
# Persist at the User scope (no admin needed; survives reboots).
[Environment]::SetEnvironmentVariable('TRELLO_KANBAN_BOARD_ID',     $boardId,   'User')
[Environment]::SetEnvironmentVariable('WORKSPACE_TRELLO_ROUTER_DIR', $routerDir, 'User')

# Also load into the CURRENT shell so you don't have to relaunch yet.
$env:TRELLO_KANBAN_BOARD_ID     = $boardId
$env:WORKSPACE_TRELLO_ROUTER_DIR = $routerDir
```

### bash / zsh (macOS, Linux)

Pick the profile that matches the user's login shell — `~/.bashrc`
for bash, `~/.zshrc` for zsh, or `~/.bash_profile` on macOS bash. Use
the same file the operator uses for the credentials in
[trello-credentials.md §6](trello-credentials.md#6-export-the-four-secrets);
do **not** spread them across two files.

```bash
# Persist by appending to the shell profile.
PROFILE="$HOME/.bashrc"   # or ~/.zshrc, etc.
{
    echo ""
    echo "# JJC bootstrap"
    echo "export TRELLO_KANBAN_BOARD_ID='$BOARD_ID'"
    echo "export WORKSPACE_TRELLO_ROUTER_DIR='$ROUTER_DIR'"
} >> "$PROFILE"

# Also load into the CURRENT shell.
export TRELLO_KANBAN_BOARD_ID="$BOARD_ID"
export WORKSPACE_TRELLO_ROUTER_DIR="$ROUTER_DIR"
```

> If the user prefers a per-project `.envrc` + `direnv` setup (see
> [SKILL.md §6](../SKILL.md)), append the same two `export …` lines to
> `.envrc` instead and run `direnv allow`.

Re-run the detection snippet from
[trello-credentials.md §1](trello-credentials.md) afterwards — both
new variables should report `set (len=…)` with non-zero lengths.

---

## 7. Tell the user what to do next

Hand the operator three things, in this order:

1. **Pick up the persisted env vars in fresh shells.** The two new
   values only auto-load in shells started **after** §6 ran:
   - **Windows:** close the current PowerShell window and open a new
     one. The User-scope env vars are read at shell start, so the new
     window inherits `TRELLO_KANBAN_BOARD_ID` and
     `WORKSPACE_TRELLO_ROUTER_DIR` automatically. The *current* window
     already has them (§6 loaded them with `$env:…`), so you can also
     stay where you are.
   - **macOS / Linux:** either `source ~/.bashrc` (or whichever
     profile §6 wrote to) in any existing shell, or open a new
     terminal. The current shell, again, already has them from the
     `export …` lines in §6.

2. **Hand them the board URL.** Use the `$board.url` / `$BOARD_URL`
   printed in §3 — opening it in a browser is the fastest way for the
   operator to confirm the seven lists landed in the expected order.

3. **Start `jjc`.** All required env vars are now set except
   `--playbooks-dir`, which has no sensible default. Pick a directory
   that contains the operator's `.md` playbooks (e.g. a clone of the
   repo's [`playbook/`](../../../../playbook/) directory, or a private
   collection) and launch:

   ```powershell
   jjc --playbooks-dir 'C:\path\to\playbooks'
   ```

   ```bash
   jjc --playbooks-dir /path/to/playbooks
   ```

   A healthy startup logs `event=gateway_starting` with every value
   redacted to a length fingerprint, followed by `event=*` lines for
   the Cloudflare quick tunnel, webhook reconciliation, and the HTTP
   listener (see [SKILL.md §7](../SKILL.md)).

---

## 8. Common pitfalls

- **Lists created out of order.** `pos=bottom` appends to the right.
  If the script in §3 was interrupted partway through, archive the
  partially-created board (board settings → *Close board*) and re-run
  the whole script rather than trying to splice lists in by hand —
  Trello's `pos` values are floats and re-ordering is fiddly.
- **`401 invalid token` from the board create.** Almost always means
  `TRELLO_API_TOKEN` was revoked or the wrong value was copied (the
  API *key* pasted into the *token* slot is the classic mistake). Re-do
  [trello-credentials.md §4 step 6-7](trello-credentials.md#4-get-the-api-key-and-token).
- **`raw.githubusercontent.com` blocked.** Falls back to copying
  `examples/router/router.hcl` from a local repo checkout — see the
  callout in §5.
- **New PowerShell window still missing the vars.** §6 must have used
  scope `'User'`. Process-scope (the default for `setx /M …` style
  commands) does not survive a new shell. Re-run the
  `SetEnvironmentVariable` lines with `'User'` exactly as written.
- **Wrong shell profile on *nix.** Appending to `~/.bashrc` on a host
  whose login shell is zsh means the vars never load. Check
  `echo $SHELL` first; if it ends in `zsh`, write to `~/.zshrc`
  instead.
