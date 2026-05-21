# Installing `cloudflared`

JJC's default tunnel provider is `--tunnel=cloudflared`. When this is
selected, JJC runs `cloudflared tunnel --url http://localhost:<listen>`,
waits for the `https://*.trycloudflare.com/` URL, and reconciles the
Trello board webhook to it. The binary must be installed and
discoverable on `PATH` before JJC starts; otherwise the gateway fails
fast with a `cloudflared not found` style error.

Authoritative upstream reference:
<https://github.com/cloudflare/cloudflared#installing-cloudflared> and
<https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/>.

---

## 1. Check whether it's already installed

Run this first; only proceed to install if it fails.

### Windows (PowerShell)

```powershell
cloudflared --version
```

If the command resolves and prints `cloudflared version 2026.x.x …`,
you're done — skip the install steps.

### macOS / Linux

```bash
cloudflared --version
```

Same rule: a printed version means you're done.

If the shell reports "not recognized" / "command not found", continue
to §2.

---

## 2. Install

Pick the path that matches the host OS. JJC only needs the standalone
`cloudflared` binary on `PATH`; you do **not** need to log in
(`cloudflared tunnel login`) or create a named tunnel — JJC uses the
account-less TryCloudflare quick-tunnel flow.

### Windows

Use the official `.msi` installer (signed, adds `cloudflared` to the
system `PATH`):

1. Open <https://github.com/cloudflare/cloudflared/releases/latest> and
   download `cloudflared-windows-amd64.msi` (or `arm64` if applicable).
2. Run the MSI; accept the defaults.
3. Open a **new** PowerShell window so it picks up the updated `PATH`,
   then verify:

   ```powershell
   cloudflared --version
   ```

Alternative (no admin): download `cloudflared-windows-amd64.exe` from
the same release page, rename it to `cloudflared.exe`, drop it into a
directory already on `PATH` (e.g. `%USERPROFILE%\bin\`), and verify.

### macOS

Homebrew is the recommended path:

```bash
brew install cloudflared
cloudflared --version
```

No-Homebrew alternative: download the `cloudflared-darwin-amd64.tgz`
or `cloudflared-darwin-arm64.tgz` from
<https://github.com/cloudflare/cloudflared/releases/latest>, extract,
and move the binary into a `PATH` directory:

```bash
tar -xzf cloudflared-darwin-arm64.tgz
sudo mv cloudflared /usr/local/bin/
cloudflared --version
```

### Linux

Use the official package for your distro from
<https://pkg.cloudflare.com/index.html> (see Cloudflare's
[downloads page](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/#linux)
for the full apt / rpm repo setup).

Quick fall-back (works on any glibc Linux without configuring a repo):

```bash
# amd64; replace with arm64 / arm if needed
curl -L --fail \
  https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 \
  -o cloudflared
chmod +x cloudflared
sudo mv cloudflared /usr/local/bin/
cloudflared --version
```

---

## 3. Confirm JJC will find it

Once `cloudflared --version` works in a **fresh shell** (i.e. the one
that will launch `jjc`), the default startup path is ready. To
sanity-check the quick-tunnel flow without JJC, run:

```bash
cloudflared tunnel --url http://localhost:9999
```

You should see lines like:

```
INF Requesting new quick Tunnel on trycloudflare.com...
INF |  Your quick Tunnel has been created! Visit it at (it may take some time to be reachable):  |
INF |  https://<random-words>.trycloudflare.com                                                  |
```

Stop it with `Ctrl+C`. The same flow runs automatically when JJC starts
with the default `--tunnel=cloudflared`.

---

## 4. Opting out

If the host already has a stable public domain pointing at the JJC
listen port (reverse proxy, ingress, etc.), you don't need
`cloudflared` at all. Skip this entire reference and start JJC with:

```bash
jjc --tunnel=none --callback-url "https://your-public-domain/" …
```

See [SKILL.md §7](../SKILL.md) for the full production-style startup
recipe.

---

## 5. Common pitfalls

- **Installed but not on `PATH`.** Common when dropping the `.exe`
  outside any `PATH` directory on Windows, or extracting the tarball
  into `~/Downloads` on macOS / Linux. Move the binary to a `PATH`
  directory (or add its directory to `PATH`) — JJC does not accept an
  explicit path to `cloudflared`.
- **Old version.** Cloudflare only supports releases within the last
  ~12 months. If `cloudflared --version` reports something older than
  one year, upgrade via the same install method.
- **Corporate firewall blocking `*.trycloudflare.com`.** The quick
  tunnel needs outbound UDP/QUIC + HTTPS to Cloudflare edge IPs. If
  these are blocked, use `--tunnel=none` + a separately provisioned
  public URL instead.
- **Multiple `cloudflared` binaries.** A leftover manual download plus a
  Homebrew / MSI install can disagree. Use `which -a cloudflared`
  (Unix) or `Get-Command -All cloudflared` (PowerShell) to spot
  duplicates and remove the older one.
