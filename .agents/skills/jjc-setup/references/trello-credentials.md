# Trello account, API key, token, and webhook secret

JJC needs three Trello secrets exported to the shell before it can
start:

```bash
TRELLO_API_KEY=…       # identifies your Trello "Power-Up" (app)
TRELLO_API_TOKEN=…     # OAuth-style token that grants the app access to your boards
TRELLO_API_SECRET=…    # shared secret used to HMAC-SHA1 sign webhook deliveries
```

Plus the kanban board id (see [SKILL.md §2](../SKILL.md) wrap-up):

```bash
TRELLO_KANBAN_BOARD_ID=…   # 24-hex id from the board URL
```

This reference walks through producing each of those values.

---

## 1. Detect what is already exported

Run this **first** before walking the user through any account / Power-Up
setup — many hosts already have these from a previous JJC install or
from a coworker's `.env`.

### PowerShell (Windows)

```powershell
'TRELLO_API_KEY', 'TRELLO_API_TOKEN', 'TRELLO_API_SECRET', 'TRELLO_KANBAN_BOARD_ID' |
    ForEach-Object {
        $v = [Environment]::GetEnvironmentVariable($_)
        if ([string]::IsNullOrEmpty($v)) {
            [PSCustomObject]@{ Name = $_; Status = 'MISSING'; Length = 0 }
        } else {
            [PSCustomObject]@{ Name = $_; Status = 'set'; Length = $v.Length }
        }
    } | Format-Table -AutoSize
```

### bash / zsh (macOS, Linux)

```bash
for v in TRELLO_API_KEY TRELLO_API_TOKEN TRELLO_API_SECRET TRELLO_KANBAN_BOARD_ID; do
    if [ -z "${!v}" ]; then
        printf '%-26s MISSING\n' "$v"
    else
        printf '%-26s set (len=%d)\n' "$v" "${#!v}"
    fi
done
```

> Never print the values themselves — only their presence and length.
> JJC's own startup log redacts these to length fingerprints for the
> same reason.

**If all four are set**, jump to [SKILL.md §6](../SKILL.md) to confirm
the values are wired into the shell that will launch `jjc`.

**If any are missing**, continue with §2.

---

## 2. Do you have a Trello account?

Ask the user:

> Do you already have a Trello account you want JJC to drive?

- **No / not sure** → §3.
- **Yes** → skip to §4.

---

## 3. Create a Trello account

Direct the user to Trello's sign-up page:

- Sign-up: <https://trello.com/signup>
- (Alternative entry point: <https://trello.com/> → "Sign up")

Trello accepts Google / Microsoft / Apple SSO or a plain
email + password. A free workspace is enough — JJC only needs one
board.

After the account exists:

1. Create a workspace (Trello asks during onboarding; any name works).
2. Create a new board inside that workspace. The default "Kanban"
   template is fine; JJC will rename / re-purpose the columns through
   `router.hcl` later. Note the **board URL** — the 24-hex segment
   right after `/b/` (or visible in *Show menu → … More → Print and
   export → Export as JSON* → `id`) becomes `TRELLO_KANBAN_BOARD_ID`.

Then proceed to §4.

---

## 4. Get the API key and token

Trello issues the API key + token from the developer Power-Up page,
which requires the account from §3 to be logged in.

1. Open <https://trello.com/power-ups/admin> while logged in.
2. **New** → fill in a workspace and a name (e.g. `jjc-gateway`),
   accept the terms, **Create**.
3. On the new Power-Up's page, open the **API key** tab.
4. Copy the **API key** — this is `TRELLO_API_KEY` (32-char hex).
5. Copy the **OAuth secret** shown right under the key — this is
   `TRELLO_API_SECRET`. *Trello calls it "OAuth secret"; JJC calls it
   the webhook signing secret because the same shared secret is used
   to verify `HMAC-SHA1(secret, raw_body + callbackURL)` on every
   incoming webhook.*
6. Click the **Token** link (just above the key) to open a generated
   URL like `https://trello.com/1/authorize?…&key=<API_KEY>&…`.
7. On the resulting page, click **Allow**. Trello redirects to a page
   showing a long token; copy it — this is `TRELLO_API_TOKEN`.

The token Trello issues here is account-scoped and never expires until
the user revokes it from
<https://trello.com/<username>/account> → **Power-Ups → Revoke**. Treat
it like a password.

> **Ask the user for one secret at a time.** Step 4 (API key), step 5
> (OAuth secret), and step 7 (API token) each produce a single value
> on a separate Trello screen. Prompt the user for **only the value
> they just copied** and wait for them to reply before walking them to
> the next step. A combined "please paste key, secret, and token"
> message almost always comes back partial or with two values swapped,
> and because secrets are redacted in the next steps you cannot tell
> which one is wrong. One step, one ask, one acknowledgement.

---

## 5. Capture the board id

> **No board yet?** A brand-new Trello account (or one that has never
> hosted a kanban board) won't have an id to capture. Offer to bootstrap
> one for the user — the full procedure (create board, create the seven
> JJC-shaped lists, generate `router-dir/router.hcl`, persist both
> `TRELLO_KANBAN_BOARD_ID` and `WORKSPACE_TRELLO_ROUTER_DIR`, hand the
> user the next-step commands) is in
> [bootstrap-board-and-router.md](bootstrap-board-and-router.md). Run
> it now, then skip the rest of this section.

JJC needs the 24-hex board id, not the short slug.

- Open the board in a browser. The URL looks like
  `https://trello.com/b/<shortLink>/<slug>`.
- Add `.json` to the URL — `https://trello.com/b/<shortLink>/<slug>.json`
  — and look for `"id": "…"` near the top. That value is
  `TRELLO_KANBAN_BOARD_ID`.

(Power-Up admins can also see it via the JSON export from §3 step 2.)

---

## 6. Export the four secrets

Put the four values into the shell that will launch `jjc`. Two safe
patterns:

### Per-session (recommended while iterating)

PowerShell:

```powershell
$env:TRELLO_API_KEY            = '<paste-key-here>'
$env:TRELLO_API_TOKEN          = '<paste-token-here>'
$env:TRELLO_API_SECRET         = '<paste-oauth-secret-here>'
$env:TRELLO_KANBAN_BOARD_ID    = '<24-hex-board-id>'
```

bash / zsh:

```bash
export TRELLO_API_KEY='<paste-key-here>'
export TRELLO_API_TOKEN='<paste-token-here>'
export TRELLO_API_SECRET='<paste-oauth-secret-here>'
export TRELLO_KANBAN_BOARD_ID='<24-hex-board-id>'
```

### Persisted (only after the per-session run works)

- Linux / macOS: append the same `export` lines to `~/.bashrc` /
  `~/.zshrc`, or — better — keep them in a per-project `.env` loaded
  by `direnv`. Never commit the file.
- Windows: use `[Environment]::SetEnvironmentVariable(name, value, 'User')`
  to store at the user scope (no admin needed). Equivalent GUI path:
  *System Properties → Environment Variables → User variables*.

After exporting, re-run the detection snippet from §1 in a fresh shell
to confirm all four show `set` with non-zero length.

---

## 7. Verify with a one-liner sanity probe

Optional but useful — confirms the key + token actually authenticate
against Trello before JJC tries:

PowerShell:

```powershell
Invoke-RestMethod "https://api.trello.com/1/members/me?key=$env:TRELLO_API_KEY&token=$env:TRELLO_API_TOKEN" |
    Select-Object id, username, fullName
```

bash / zsh:

```bash
curl -s "https://api.trello.com/1/members/me?key=$TRELLO_API_KEY&token=$TRELLO_API_TOKEN" |
    jq '{id, username, fullName}'
```

A `200 OK` with your Trello identity → key + token are valid.
`401 invalid token` → re-do §4 step 6-7 (the most common slip is
copying the key instead of the token from the redirect URL).

`TRELLO_API_SECRET` cannot be probed this way — its only use is HMAC
verification of inbound webhooks, which JJC exercises on the first
real Trello event after startup. A `signature_invalid` log line at
that point means the secret in the env is wrong.

---

## 8. Common pitfalls

- **Secret confusion.** The "OAuth secret" on the Power-Up page is the
  *webhook signing* secret JJC calls `TRELLO_API_SECRET`. It is **not**
  the user token. The token is the long string from the `Allow` page
  redirect in §4 step 7.
- **Wrong board id.** The short `/b/<shortLink>/` segment is not the
  id JJC needs. Always grab the 24-hex value from
  `https://trello.com/b/<shortLink>/<slug>.json`.
- **Token revoked.** If `Invoke-RestMethod` / `curl` in §7 returns
  `401 invalid token`, the user (or a coworker) has revoked the token
  from <https://trello.com/<username>/account>. Re-issue a new one via
  §4 step 6-7.
- **Pasting from rich-text apps.** Slack / Teams sometimes inject a
  trailing zero-width space. If the env var "looks right" but JJC
  reports an auth error, re-paste the value into a plain text editor
  first.
- **Free-tier rate limits.** A free Trello workspace caps the API at
  ~300 requests per 10 s per token. JJC stays far below that for normal
  workloads, but a noisy `event=trello_*` loop after a router
  mis-configuration can trip it. The fix is the configuration, not
  bumping the limit.
