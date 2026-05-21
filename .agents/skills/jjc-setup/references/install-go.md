# Installing Go and configuring `go install`

This is the first prerequisite for [SKILL.md §1](../SKILL.md). JJC is a
Go program; the `jjc` binary is normally produced by either
`go install github.com/lonegunmanb/jjc@latest` or a local
`go build ./...` from a clone. Both paths require a working Go toolchain
on `PATH` and a `go install` bin directory that your shell can find.

Authoritative upstream reference: <https://go.dev/doc/install>.

---

## 1. Install the Go toolchain

Pick the path that matches your OS. JJC currently builds and tests
against the `go` version declared in [go.mod](../../../go.mod) — install
that version or newer.

### Windows

1. Download the MSI installer from <https://go.dev/dl/> (pick the
   `windows-amd64` `.msi`).
2. Run the installer. The default install location is
   `C:\Program Files\Go`, and the installer adds `C:\Program Files\Go\bin`
   to the **system** `PATH` automatically.
3. Open a **new** PowerShell window (so it picks up the updated `PATH`)
   and verify:

   ```powershell
   go version
   ```

   You should see something like `go version go1.24.x windows/amd64`.

### macOS

1. Download the `darwin-arm64` or `darwin-amd64` `.pkg` from
   <https://go.dev/dl/> and run it (installs to `/usr/local/go`,
   PATH-wired via `/etc/paths.d/go`).
2. Alternatively: `brew install go`.
3. In a new terminal:

   ```bash
   go version
   ```

### Linux

1. Download the `linux-amd64` (or `linux-arm64`) tarball from
   <https://go.dev/dl/>.
2. Remove any old install and extract to `/usr/local`:

   ```bash
   sudo rm -rf /usr/local/go
   sudo tar -C /usr/local -xzf go1.24.x.linux-amd64.tar.gz
   ```

3. Add `/usr/local/go/bin` to `PATH` in your shell profile
   (`~/.bashrc`, `~/.zshrc`, `~/.profile`):

   ```bash
   export PATH=$PATH:/usr/local/go/bin
   ```

4. Reload the profile (`source ~/.bashrc`) and verify:

   ```bash
   go version
   ```

> Distribution packages (`apt install golang-go`, `dnf install golang`)
> often lag behind the Go release cycle by months. If `go version`
> reports a release older than the one in [go.mod](../../../go.mod),
> install from <https://go.dev/dl/> instead.

---

## 2. Where `go install` puts binaries

`go install <pkg>@<version>` compiles a package's `main` and drops the
resulting executable in a single directory, chosen with this priority:

| Priority | Variable                | Resolved install dir                |
| -------- | ----------------------- | ----------------------------------- |
| 1        | `$GOBIN`                | exactly `$GOBIN`                    |
| 2        | first entry of `$GOPATH`| `$GOPATH/bin`                       |
| 3        | (neither set)           | `$HOME/go/bin` (Unix) / `%USERPROFILE%\go\bin` (Windows) |

Check what your toolchain has actually resolved:

```bash
go env GOBIN GOPATH
```

If `GOBIN` is empty, the binary will land in
`<first GOPATH entry>/bin`. On a fresh install that's `~/go/bin` (Unix)
or `%USERPROFILE%\go\bin` (Windows).

---

## 3. Add the install dir to `PATH`

Without this step, `jjc` is built successfully but the shell can't find
it. The fix is one line per shell profile.

### Linux and macOS (bash / zsh)

Append to `~/.bashrc`, `~/.zshrc`, or `~/.profile`:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

Reload:

```bash
source ~/.bashrc   # or ~/.zshrc, ~/.profile
```

If you set `$GOBIN` explicitly, append that instead:

```bash
export GOBIN=$HOME/.local/bin
export PATH=$PATH:$GOBIN
```

### Windows (PowerShell, per-user)

1. Open **Settings → System → About → Advanced system settings →
   Environment Variables** (or run `sysdm.cpl` → *Advanced* →
   *Environment Variables*).
2. Under **User variables**, edit `Path`.
3. Add a new entry: `%USERPROFILE%\go\bin` (or your `GOBIN` value).
4. Click OK, **open a new PowerShell window**, and confirm:

   ```powershell
   $env:Path -split ';' | Select-String 'go\\bin'
   ```

Scripted alternative (per-user, no restart needed for new shells):

```powershell
$gopath = (go env GOPATH)
$bin    = Join-Path $gopath 'bin'
$old    = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($old -notlike "*${bin}*") {
    [Environment]::SetEnvironmentVariable('Path', "$old;$bin", 'User')
}
```

---

## 4. Verify end-to-end with `go install`

A round-trip check that proves toolchain + `PATH` are wired correctly:

```bash
# 1. Install a tiny, well-known tool.
go install golang.org/x/example/hello@latest

# 2. Run it from any directory.
hello
# -> Hello, Go examples!
```

If step 1 succeeds but step 2 reports "command not found" /
"is not recognized as the name of a cmdlet", your `PATH` is missing the
install dir from §3.

When that round-trip works, you are ready for SKILL.md §4
(**Build or install the binary**), where the actual command is:

```bash
go install github.com/lonegunmanb/jjc@latest
```

and the resulting `jjc` (or `jjc.exe`) is the same install-dir binary
you just learned to put on `PATH`.

---

## 5. Common pitfalls

- **Two Go versions on PATH.** A leftover distribution package plus a
  `go.dev/dl` install will silently disagree. Run `which -a go` (Unix)
  or `Get-Command -All go` (PowerShell) to spot duplicates; remove the
  older one.
- **`go install` succeeds, `jjc` not found.** The build worked; `PATH`
  is wrong. Re-do §3 in a new shell.
- **`GOPATH` has multiple entries.** `go install` only uses the first
  one for `bin/`. Either set `GOBIN` explicitly, or make sure the first
  `GOPATH` entry's `bin/` is on `PATH`.
- **Module proxy blocked.** Behind a strict corporate network,
  `go install ... @latest` may fail to reach `proxy.golang.org`. Set
  `GOPROXY` to your internal proxy (e.g.
  `GOPROXY=https://goproxy.example.com,direct`) before retrying.
- **Stale binary after upgrade.** `go install` overwrites the binary in
  place; if you still see the old version, you're running a different
  copy. Re-check §2 — most often `GOBIN` was set in one shell and not
  in another.
