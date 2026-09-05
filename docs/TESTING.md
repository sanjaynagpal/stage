# Testing Stage

This is a practical runbook for testing the work so far. See `docs/DESIGN.md` for architecture
and `docs/REQUIREMENTS.md` for the rationale behind specific behaviors. Everything here has been
run and verified on a real Windows machine.

## Prerequisites

- Windows (this repo builds Windows-only — registry/syscall/WinHTTP code throughout).
- Go (matching `go.mod`'s version).
- A JDK on this machine, with `JAVA_HOME` set to it. It's used as the manual test harness's
  "bundled" JRE — no real JRE archive is committed to the repo (too large; see `.gitignore`).

## 1. Automated tests

```powershell
go build ./...      # confirms everything compiles
go vet ./...
go test ./...        # unit tests across every internal/ package
```

Run a single package or test while iterating:
```powershell
go test ./internal/manifest/... -v
go test ./internal/proxydetect/ -run TestDetectForURL -v
```

`internal/proxydetect`, `internal/diskspace`, and `internal/procscan` each include a test that
calls the real Windows API (not mocked) — they assert structural sanity and log the result
rather than asserting a fixed value, since actual proxy/disk/process state is machine-dependent.

## 2. Manual end-to-end test (exercises the real `rigger.exe` launch flow)

There's no installer yet (`cmd/stagebuild`/`cmd/installer` are still placeholders), so
`examples/abc/` stands in for one: a fixture Java app, a fake manifest server, and a script that
assembles a real per-user install.

### 2.1 Set up the install

From the repo root:
```powershell
.\examples\abc\setup-dev-install.ps1
```

This builds `rigger.exe`, builds the fixture app's jar, copies your local JDK in as the "bundled"
JRE, writes the manifest (local cache + fakeserver-served copy), and writes the
`HKCU:\Software\ABC` registry values — everything a real installer would do for this purpose. It
prints the exact follow-up commands, ending in something like:

```
Done. Install root: C:\Users\<you>\AppData\Local\ABC

Next steps:
  1. Start fakeserver (separate shell):
       go run ./examples/abc/fakeserver -dir examples/abc/fakeserver/served
  2. Launch via shortcut path:
       & 'C:\Users\<you>\AppData\Local\ABC\rigger.exe'
  3. Launch via protocol-handler path (token gets injected as -Dabc.token):
       & 'C:\Users\<you>\AppData\Local\ABC\rigger.exe' 'acme-abc://launch?token=demo123'
  4. Same, but with a mismatched Network Zone to see the consistency-check warning:
       & 'C:\Users\<you>\AppData\Local\ABC\rigger.exe' 'acme-abc://launch?token=demo123&networkZone=Radianz'
  5. Log written to: C:\Users\<you>\AppData\Roaming\ABC\abc-launch.log
  6. Rigger's own log: C:\Users\<you>\AppData\Roaming\ABC\rigger.log
```

Re-run this script any time you rebuild `rigger.exe` or change the fixture manifest — it's
idempotent (skips the JDK copy if it's already there).

### 2.2 Start the fake manifest server

In a separate shell, from the repo root:
```powershell
go run ./examples/abc/fakeserver -dir examples/abc/fakeserver/served
```
Leave this running. It logs every request it receives, so you can watch Rigger's manifest
fetches happen live.

### 2.3 Launch it

**Shortcut path** (no arguments — the normal Start Menu/Desktop launch):
```powershell
& "$env:LocalAppData\ABC\rigger.exe"
```
Expect console output like:
```
rigger: launching ABC v1.0.0 (invoked via shortcut)
rigger: loaded manifest v1.0.0 (fresh fetch from http://127.0.0.1:8080/abc/manifest.json)
rigger: using Java 25.0.1 at C:\Users\<you>\AppData\Local\ABC\jre\25.0.1
rigger: launching ABC 1.0.0 (mainClass=com.example.abc.Main)
```
...and a Swing dialog from the ABC fixture app should appear, reporting the args/system
properties Rigger resolved. Close the dialog and kill the `javaw.exe` process before the next
launch (there's no supervision — Rigger execs and exits, so nothing does this for you):
```powershell
Get-Process javaw | Stop-Process -Force
```

**Protocol-handler path** (simulates the browser-based auth redirect, §11):
```powershell
& "$env:LocalAppData\ABC\rigger.exe" "acme-abc://launch?token=demo123"
```
The dialog/log should now show `abc.token = demo123` instead of `(not set)`.

**Zone-mismatch warning** (simulates an auth server returning a different zone than this
install's configured one):
```powershell
& "$env:LocalAppData\ABC\rigger.exe" "acme-abc://launch?token=demo123&networkZone=Radianz"
```
Expect an extra line:
```
rigger: warning: auth response Network Zone "Radianz" does not match this install's configured zone "Internet"
```

**Offline fallback** (stop the fakeserver first, `Ctrl+C` in its shell, then launch again): the
app should still launch normally, using the cached manifest, with a line noting the fetch
failure — proving connectivity is only needed to *check* for updates, never to launch (§9b).

**Forced fatal error** (to see the missing-JRE path): rename
`%LocalAppData%\ABC\jre\<version>` to something else, then launch. Expect a message box titled
"Application Launch Failed" (dismiss it to let the process exit) and a matching `FATAL` line in
`rigger.log` — but only **one** copy of the error, not a duplicate on top of the message box.
Rename the JRE directory back afterward.

### 2.4 Check the logs

Two files accumulate across every launch (they append, not overwrite — launch twice in a row
and confirm the line count grows):

- `%AppData%\ABC\rigger.log` — Rigger's own tiered log: invocation kind, manifest load
  outcome (fresh fetch vs. cached fallback, with the fetch error if any), resolved Java
  version/path, the final launch line, and any `WARN`/`FATAL` entries.
- `%AppData%\ABC\abc-launch.log` — the fixture Java app's own report of what it received
  (arguments and the `abc.*` system properties Rigger injected).

### 2.5 Clean up

Kill any leftover processes after testing:
```powershell
Get-Process javaw, fakeserver, rigger -ErrorAction SilentlyContinue | Stop-Process -Force
```
(`go run` also leaves a `go.exe` parent process behind if you started `fakeserver` that way —
`Ctrl+C` in its shell is cleaner than killing it externally.)
