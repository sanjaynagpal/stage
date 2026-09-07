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

This builds `rigger.exe`, builds the fixture app's jar and zips it into the artifacts archive
fakeserver serves (deliberately *not* also copied into the install's version directory — see
below), copies your local JDK in as the "bundled" JRE, writes the manifest (local cache +
fakeserver-served copy), and writes the `HKCU:\Software\ABC` registry values — everything a real
installer would do for this purpose. It prints the exact follow-up commands, ending in something
like:

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
The install's `1.0.0` version directory doesn't exist yet (`setup-dev-install.ps1` deliberately
didn't create it — see §2.1), so this first launch also exercises the on-demand jar fetch
(`internal/jarprovision`). Expect console output like:
```
rigger: launching ABC v1.0.0 (invoked via shortcut)
rigger: loaded manifest v1.0.0 (fresh fetch from http://127.0.0.1:8080/abc/manifest.json)
rigger: using Java 25.0.1 at C:\Users\<you>\AppData\Local\ABC\jre\25.0.1
rigger: application version 1.0.0 not found locally at C:\Users\<you>\AppData\Local\ABC\1.0.0 — fetching from http://127.0.0.1:8080/abc/artifacts/1.0.0.zip
rigger: fetched and installed application version 1.0.0
rigger: launching ABC 1.0.0 (mainClass=com.example.abc.Main)
```
...and a Swing dialog from the ABC fixture app should appear, reporting the args/system
properties Rigger resolved. Close the dialog and kill the `javaw.exe` process before the next
launch (there's no supervision — Rigger execs and exits, so nothing does this for you):
```powershell
Get-Process javaw | Stop-Process -Force
```
Launch a second time and confirm the fetch lines are gone — `1.0.0` now exists on disk, so
Rigger launches straight from it. To re-test the fetch path, delete
`$env:LocalAppData\ABC\1.0.0` and launch again.

**Jar-fetch checksum mismatch** (to see the archive get rejected): after deleting `1.0.0` as
above, edit `examples/abc/fakeserver/served/abc/manifest.json`'s `artifactSha256` to any other
64-hex-char value, then launch. Expect a fatal error naming the checksum mismatch, and confirm
`1.0.0` was not created. Restore the correct checksum afterward (rerun
`setup-dev-install.ps1`, which recomputes and rewrites it).

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

### 2.5 Testing on-demand JRE provisioning (Dynamic package mode)

`setup-dev-install.ps1` (§2.1) already sets `PackageMode=Dynamic` in the registry and, alongside
the local `jre/<version>` copy, zips the same JDK into a servable archive at
`examples/abc/fakeserver/served/abc/jre/<version>-win-x64.zip` with the manifest's
`runtime.sha256` set to its real checksum — everything needed to exercise Rigger's on-demand,
in-process JRE fetch (`internal/jreprovision.Provision`, docs/REQUIREMENTS.md §12-14, §22) without
a second JDK version.

**Trigger a real fetch**: with fakeserver running (§2.2), delete the local JRE directory and
launch:
```powershell
Remove-Item -Recurse -Force "$env:LocalAppData\ABC\jre\<version>"
& "$env:LocalAppData\ABC\rigger.exe"
```
Expect console output like:
```
rigger: launching ABC v1.0.0 (invoked via shortcut)
rigger: loaded manifest v1.0.0 (fresh fetch from http://127.0.0.1:8080/abc/manifest.json)
rigger: required Java runtime <version> not found at C:\Users\<you>\AppData\Local\ABC\jre\<version> — fetching from http://127.0.0.1:8080/abc/jre/<version>-win-x64.zip
rigger: provisioned Java runtime <version>
rigger: using Java <version> at C:\Users\<you>\AppData\Local\ABC\jre\<version>
rigger: launching ABC 1.0.0 (mainClass=com.example.abc.Main)
```
All of this happens inside the single `rigger.exe` process — there's no companion exe and no
second log source; `rigger.log` shows the same lines as the console.

**Static package mode fails immediately instead of fetching**: set
`HKCU:\Software\ABC\PackageMode` to `Static` (`Set-ItemProperty -Path HKCU:\Software\ABC -Name
PackageMode -Value Static`), delete the JRE directory again, and launch. Expect an immediate
fatal error naming the missing runtime with no fetch attempt logged, matching the pre-existing
failure behavior. Set `PackageMode` back to `Dynamic` afterward.

**Checksum mismatch**: with `PackageMode` back to `Dynamic` and the JRE directory deleted, edit
`examples/abc/fakeserver/served/abc/manifest.json`'s `runtime.sha256` to any other 64-hex-char
value, then launch. Expect a fatal error naming the checksum mismatch, and confirm the JRE
directory was not created. Restore the correct checksum afterward (rerun
`setup-dev-install.ps1`, which recomputes and rewrites it).

### 2.5b Testing jars-mode delivery (individual jars instead of one zip)

`manifest.Manifest.Jars` is the per-file alternative to `ArtifactSHA256`'s single zip archive — an
app declares its jars individually and Rigger fetches each one separately
(`internal/jarprovision.ProvisionJars`) instead of one archive. Re-run the setup script with the
switch to exercise this path instead of the default:
```powershell
.\examples\abc\setup-dev-install.ps1 -JarsMode
```
This copies `app.jar` as a loose file to `examples/abc/fakeserver/served/abc/artifacts/1.0.0/app.jar`
(matching `Manifest.JarsBaseURL()`'s `.../artifacts/<version>/<path>` convention) instead of
zipping it, and writes `"jars": [ { "path": "app.jar", "sha256": "..." } ]` into the manifest
instead of `artifactSha256`. Everything else — registry values, JRE — is identical to §2.1.

With fakeserver running (§2.2) and `$installRoot\1.0.0` deleted (or freshly set up, which never
creates it), launch as in §2.3. Expect console output like:
```
rigger: application version 1.0.0 not found locally at C:\Users\<you>\AppData\Local\ABC\1.0.0 — fetching 1 jar(s) from http://127.0.0.1:8080/abc/artifacts/1.0.0/
rigger: Downloading app.jar (1/1)...
rigger: downloading app.jar — 20% (0.0/0.0 MB)
rigger: downloading app.jar — 100% (0.0/0.0 MB)
rigger: fetched and installed application version 1.0.0 at C:\Users\<you>\AppData\Local\ABC\1.0.0
```
(Verified against a real run — even the tiny fixture jar crosses the progress throttle at least
twice; a larger jar shows more intermediate percentages.) With more than one entry in `Jars`,
expect one `"Downloading <path> (<index>/<total>)..."` line per jar, in declaration order. A
browser tab also opens ("Opening progress in your browser: ..."), showing the same milestones/
percentage live — the `internal/wizard.ShowProgress` page any on-demand JRE/jar fetch opens.

**Checksum mismatch**: with `1.0.0` deleted, edit `examples/abc/fakeserver/served/abc/manifest.json`'s
`jars[0].sha256` to any other 64-hex-char value, then launch. Expect a fatal error naming the
mismatch for `app.jar` specifically, and confirm `1.0.0` was not created. Restore the correct
checksum afterward (rerun `setup-dev-install.ps1 -JarsMode`).

Switch back to the default zip mode by rerunning the script without `-JarsMode`.

### 2.6 Clean up

Kill any leftover processes after testing:
```powershell
Get-Process javaw, fakeserver, rigger -ErrorAction SilentlyContinue | Stop-Process -Force
```
(`go run` also leaves a `go.exe` parent process behind if you started `fakeserver` that way —
`Ctrl+C` in its shell is cleaner than killing it externally.)

## 3. Manual end-to-end test: `cmd/installer`'s two UIs

`internal/tui` (default) and `internal/wizard` (`-gui`) drive the same install logic
(docs/DESIGN.md §2.9d) — neither is meaningfully testable without a real Windows machine (a real
terminal for `tui`, a real browser for `wizard`), so this is manual-only, same as `internal/wizard`'s
own `httptest` coverage only reaching the HTTP layer, not the actual rendered UX.

### 3.1 Build a real installer

```powershell
.\examples\abc\prepare-stagebuild-fixtures.ps1
go run .\cmd\stagebuild -config examples\abc\appconfig.generated.json -env PROD -zone Internet
```
Produces `dist\ABCSetup.exe`.

**Before testing "Launch now?" at the end of either UI below, start fakeserver** (separate
shell, from the repo root):
```powershell
go run .\examples\abc\fakeserver -dir examples\abc\fakeserver\served
```
Stage never bundles app jars (§9) — the installer lays down `rigger.exe`, a JRE, and the
manifest only, and Rigger fetches jars itself on first launch from the manifest server
(`internal/jarprovision`, §2.9b). If fakeserver isn't running, expect a single clear fatal error
naming a refused connection to `127.0.0.1:8080` — correct, intended behavior (§9: "there is no
fully-offline first run"), not a bug. Confirm this is the failure you see before assuming
anything else is wrong; `%AppData%\ABC\rigger.log` will show exactly this.

### 3.2 Terminal UI (default)

```powershell
.\dist\ABCSetup.exe
```
Expect a styled, bordered box (title in color, rounded border) showing the fixture license text
and a `[y/N]` prompt. Walk through: license → package-mode confirm → proxy override (press Enter
to accept the detected value) → extraction (status lines accumulate below the box) → "Launch ABC
now?". Confirm the install completes (`HKCU:\Software\ABC` populated, `%LocalAppData%\ABC`
extracted, Start Menu/Desktop shortcuts created) and that Ctrl+C at any prompt exits the process
immediately rather than hanging.

### 3.3 Browser wizard (`-gui`)

```powershell
.\dist\ABCSetup.exe -gui
```
Expect a line printed to the console (`Opening setup in your browser: http://127.0.0.1:<port>/<token>/`)
and an Edge "app window" (no address bar/tabs) opening to the same license screen. Click through
each screen; confirm the install reaches the same end state as §3.2. To verify the heartbeat/
cancel path, close the browser window mid-flow (e.g. at the proxy prompt) instead of answering —
expect the process to print a warning and exit within ~20 seconds rather than hang indefinitely.

### 3.4 Clean up

```powershell
Get-Process ABCSetup -ErrorAction SilentlyContinue | Stop-Process -Force
Remove-Item -Recurse -Force "$env:LocalAppData\ABC" -ErrorAction SilentlyContinue
Remove-Item -Path HKCU:\Software\ABC -Recurse -Force -ErrorAction SilentlyContinue
```
Leave any Edge windows open unless you're certain they were opened by this test — `-gui` opens a
normal `msedge.exe` process, indistinguishable from the operator's own browsing session by image
name alone.

## 4. Manual end-to-end test: `rigger.exe --doctor`

Uses the same per-user install `setup-dev-install.ps1` (§2.1) creates. Start fakeserver first
(§2.2) — the "Manifest server" check needs it running to show as reachable.

```powershell
& "$env:LocalAppData\ABC\rigger.exe" --doctor
```
Expect a console line (`Opening diagnostics in your browser: ...`) and a browser window showing
every check green: Registry, Install directory, Data directory, Manifest, Java runtime, Manifest
server, Proxy, Network zone — plus "Diagnostic logs saved to: ..." naming a new
`<DataDir>\diagnostics-<timestamp>.zip`. Confirm that zip contains both `rigger.log` and
`abc-launch.log`.

**Forced failures** (confirm doctor mode reports each clearly instead of crashing, then restore):
- Rename `%LocalAppData%\ABC\jre\<version>` — expect "Java runtime" to fail, naming the missing
  `javaw.exe` path, with every other check still shown normally.
- Stop fakeserver — expect "Manifest server" to fail with a connection error, everything else
  unaffected.
- Delete a required value from `HKCU:\Software\ABC` (e.g. `InstallScope`) — expect the page to
  show **only** a single failed "Registry" check naming reinstall as the fix, not a crash and not
  a partial report of the other checks (nothing else can be determined without a readable
  registry key).

Since the fixture manifest doesn't declare `supportEmail`, expect no "Contact Support" button —
correct, not a bug. To see it, add `"supportEmail": "support@example.com"` to
`examples/abc/fakeserver/public/abc/manifest.json` and rerun `setup-dev-install.ps1`.

## 5. Manual end-to-end test: `rigger.exe` binary self-update

Needs a genuinely different `rigger.exe` build to update *to* — building the same source twice
produces identical bytes, so this can't be tested by just running `setup-dev-install.ps1` twice.

1. Set up normally (§2.1) — `PackageMode=Dynamic` is already the default there. Note the
   installed `rigger.exe`'s checksum: `Get-FileHash "$env:LocalAppData\ABC\rigger.exe"`.
2. Temporarily bump `const Version` in `internal/riggerupdate/riggerupdate.go` (e.g. to a version
   one patch ahead), then:
   ```powershell
   go build -o examples\abc\fakeserver\served\abc\rigger\<newversion>-win-x64.exe .\cmd\rigger
   ```
   and revert the source change immediately — the built exe is a standalone artifact independent
   of source state from here on.
3. Compute its checksum (`Get-FileHash` on the file just built) and add to
   `examples/abc/fakeserver/served/abc/manifest.json`:
   ```json
   "rigger": { "version": "<newversion>", "sha256": "<checksum, lowercase>" }
   ```
4. Start fakeserver (§2.2), then launch the (still old) installed `rigger.exe`. Expect:
   ```
   rigger: loaded manifest v1.0.0 (fresh fetch from http://127.0.0.1:8080/abc/manifest.json)
   rigger: new launcher version <newversion> available (running 1.0.0) — updating
   rigger: handing off to the updated launcher
   ```
   and the process exiting immediately (exit code 0, no javaw.exe launched by *this* process).
5. Confirm: the app still launches (a `javaw.exe` process appears within a couple seconds — the
   relaunched, updated `rigger.exe` completes the normal flow), and
   `Get-FileHash "$env:LocalAppData\ABC\rigger.exe"` now matches the new build, not the original.
6. Launch again — expect **no** self-update line this time (the installed binary now matches the
   manifest's declared version); this is the check that a successful update doesn't loop.
7. **Checksum rejection**: bump the manifest's declared version once more with a deliberately
   wrong `sha256`, launch, and confirm the app still opens normally — either a logged checksum
   mismatch (if the download got that far) or, if a `%TEMP%` copy from the previous update attempt
   hasn't been cleaned up yet, a same-named-temp-file warning — both are the same designed
   fallback (§25): an update attempt must never be why the app doesn't open.
8. Clean up: remove the `examples/abc/fakeserver/served/abc/rigger/` directory and the
   `rigger`/`supportEmail` fields from the manifest if you don't want them lingering for later
   `setup-dev-install.ps1` runs (both are gitignored, so this doesn't touch anything tracked).
