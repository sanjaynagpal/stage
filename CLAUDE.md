# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

**Stage** is a Windows-only, Go-based toolkit that builds installers for Java desktop
applications. A developer runs `stagebuild` against a per-app JSON config to produce a
self-contained `<App>Setup.exe` (analogous to compiling an Inno Setup script). The installed
app is launched not directly but via **Rigger** (`rigger.exe`), a small generic launcher that
reads a JSON **manifest** to know how to invoke the JVM.

Three docs in `docs/` cover different angles and are worth distinguishing:
- `docs/REQUIREMENTS.md` — the rationale behind nearly every non-obvious decision in the code.
  Authoritative, the result of many rounds of requirements negotiation, referenced by section
  number (`§4`, `§13`, etc.) throughout code comments. **Read it before making architectural
  changes**; don't rediscover decisions that are already settled there.
- `docs/DESIGN.md` — how Stage is actually built today: architecture, data model, a full
  `internal/*` package reference, the registry/manifest schemas, and an implementation-status
  table. Prefer this over re-deriving architecture from scratch by reading files; its §7 "Known
  Gaps & Next Steps" is the living list of what's unfinished.
- `docs/TESTING.md` — the practical runbook for manually exercising `rigger.exe`'s launch flow
  end to end via `examples/abc/` (setup script, fake manifest server, expected log output for
  each launch path). Use this when verifying a change actually works, not just that tests pass.

`examples/abc/` is a sample/fixture app ("ABC") used to develop and test against — not a real
shipping product. Other real apps would reuse this same tooling by supplying their own app
config.

This is an early-stage project: several `cmd/` and `internal/` packages are currently empty
placeholder directories for planned components (see Architecture below).

## Commands

```
go build ./...      # build everything (Windows only — uses syscall/registry/win32 APIs)
go test ./...        # run all tests
go test ./internal/manifest/...   # test a single package
go test ./internal/manifest/ -run TestName -v   # run a single test
go vet ./...
```

There is no separate lint config or Makefile — plain `go build`/`go test`/`go vet` is the whole
workflow. Windows is a hard requirement: most packages call into `golang.org/x/sys/windows`
(registry, elevation tokens, disk space, process enumeration, message boxes) and won't build on
other GOOS values.

## Architecture

### Four binaries share one codebase

- **`cmd/rigger`** (implemented) — the generic, prebuilt-once launcher installed as
  `rigger.exe`. It has **zero per-app compiled state**: it derives which app it belongs to from
  its own install folder name (`layout.AppIDFromExePath`), reads that app's config from the
  registry, loads/refreshes the manifest, and execs the JVM. This is why the same `rigger.exe`
  binary works for every app Stage builds.
- **`cmd/stagebuild`** (implemented) — the per-app build tool a developer runs at build time.
  Populates `cmd/installer/payload/` (gitignored except a committed `PLACEHOLDER.txt`, needed
  because `//go:embed` can't compile against a missing/empty directory) with that app's
  `rigger.exe`/`unins.exe`/JRE archive/manifest/appconfig/icon/license, then really invokes
  `go build ./cmd/installer` — a fresh per-app compile, matching REQUIREMENTS.md §9's "analogous
  to compiling an Inno Setup script" framing, not a runtime append-bytes-to-a-stub approach.
- **`cmd/installer`** (implemented, console UI) — `//go:embed all:payload`s whatever stagebuild
  populated, then runs the real §16 flow: prerequisite checks, extraction
  (`jreprovision.ProvisionLocal`), registry writes, protocol-handler/file-association/shortcut
  registration, optional immediate launch.
- **`cmd/uninstaller`** (implemented, console UI, `unins.exe`) — self-copy-and-relaunch (a
  Windows install dir can't delete its own running exe): copies itself to `%TEMP%`, re-execs with
  `--finish-uninstall`, then that copy reads `internal/payload.InstallRecord` to know exactly what
  to unregister/remove.
Stage ships exactly two binaries per install — `rigger.exe` and `unins.exe`. There is
deliberately **no third companion exe**: on-demand JRE provisioning in Dynamic package mode
(§12-14) runs in-process inside `rigger.exe` (`internal/jreprovision.Provision`, called directly),
not via a separate `cmd/maintain` binary — see REQUIREMENTS.md §22 (Round 11), which superseded
§13's original "companion maintenance executable" call after review: every additional shipped exe
needs its own Authenticode signature and AV/SmartScreen reputation, and the constraint that
motivated a separate process (a program generally can't overwrite its own running executable)
doesn't apply to JRE provisioning, which never touches `rigger.exe`'s own file. `rigger.exe`
binary self-update itself is still unimplemented (no manifest/appconfig field yet declares a
rigger.exe version/URL/checksum); when built, it's expected to reuse `unins.exe`'s own
self-copy-to-`%TEMP%`-and-relaunch trick rather than introduce a new binary.

### Registry is the source of truth Rigger reads, not re-derives

Per `docs/REQUIREMENTS.md` §4, the guiding principle is that Rigger's own logic stays minimal —
it reads `Software\<AppId>` (via `internal/winreg`) rather than re-deriving install conventions
or walking up from its own exe path. `internal/layout` centralizes every filesystem path
convention (root dir, JRE dir, manifest path, shortcut dirs, etc.) so no other package hardcodes
them. Deliberately **not** in the registry: which JRE/app-version folders exist on disk — those
are read directly off disk so there's a single source of truth (no drift risk).

### The manifest (`internal/manifest`) drives everything Rigger does

The manifest JSON (bundled at install time, refreshed from `ManifestServerURL` thereafter) tells
Rigger which JRE to use, the classpath/main class, JVM options/arguments, and shortcut metadata.
Key behaviors to preserve when touching this package:
- `Validate()` enforces that `${...}` placeholders used in `jvmOptions`/`arguments` are either
  known built-ins (`installDir`, `dataDir`, `environment`) or `${uri.X}` where `X` is declared in
  `protocolParams` — this is a build-time safety net, not just a runtime concern.
- `Resolve()` **drops** (not empty-substitutes) a token containing an unsatisfied `${uri.X}`
  placeholder, so an unauthenticated/shortcut launch never produces a dangling `-Dfoo=` flag.
- Paths in the manifest (`runtime.path`, `classpath` entries) are always relative to the install
  root — never absolute — so the whole app folder stays xcopy-safe/relocatable.

### Package mode & install scope gate what Rigger is allowed to self-update

`InstallScope` (AllUsers/PerUser) and `PackageMode` (Static/Dynamic) are both registry values
that Rigger trusts rather than recomputes. All-users installs are always forced Static (Program
Files isn't reliably writable by ordinary later launches); only per-user installs can be
Dynamic. This switch governs *only* `rigger.exe`/JRE binary updates — jars/manifest polling via
`ManifestServerURL` is a separate, always-on channel regardless of package mode. See
`docs/REQUIREMENTS.md` §13-14 before changing anything touching update logic.

### Shared provisioning, not duplicated logic

`internal/jreprovision` has two entry points sharing one verify→extract→evict core
(`Provision` downloads from a URL; `ProvisionLocal` verifies an already-on-disk archive) —
`MaxRetainedVersions=2` eviction runs identically either way, since `cmd/installer` calling
`ProvisionLocal` handles *both* a fresh install and an upgrade-in-place re-run (a re-run of the
installer is not a "no eviction needed" special case). `cmd/rigger/main.go` calls `Provision`
directly, in-process, when a *running* Rigger finds a manifest wants a JRE version not on disk and
`PackageMode` is `Dynamic` — per §12, "JRE acquisition is not duplicated logic in Rigger" means the
routine is shared, not that it has to run out-of-process. The same download-and-verify core
(`DownloadVerified`) is also reused by `internal/jarprovision` for on-demand app-jar delivery.

### Protocol-handler launch path

Rigger can be invoked two ways: from a shortcut (no args) or via a registered custom URI scheme
(`argv[1]` is a URI like `acme-abc://launch?token=...`). `internal/uriparse.LooksLikeInvocation`
distinguishes the two by checking the URI scheme against the app's own registered
`ProtocolScheme` from the registry (not just "does it look URI-shaped" — a bare Windows drive
letter like `C:\...` parses as scheme `c`). Only params the manifest explicitly lists in
`protocolParams` ever get substituted into the JVM invocation (§10-11) — allowlisting happens at
`manifest.Resolve()` time, not in `uriparse` itself.

### Windows-native primitives, isolated per package

Each `internal/` package wraps exactly one Windows-specific concern, so callers stay portable in
theory even though this repo builds Windows-only in practice: `elevate` (admin token check),
`diskspace` (`GetDiskFreeSpaceEx`), `procscan` (Toolhelp32 snapshot, used by both the installer
and uninstaller to detect a running `rigger.exe`/`java.exe` locking the target), `winreg`
(registry read/write for both the app key and the standard Uninstall key), `uierror` (native
`MessageBox` fallback for windowsgui-subsystem binaries with no console), `proxydetect`
(WinHTTP's PAC-aware proxy detection — `golang.org/x/sys/windows` has no WinHTTP/WinINET
bindings, so this one binds `winhttp.dll` directly via `LazyDLL`/`LazyProc`), `protocolhandler`
(registers/unregisters the custom URI scheme, §11 — pure registry writes, leaf-first delete since
`registry.DeleteKey` requires an empty key), `fileassoc` (optional file-type associations, §16
step 3 — documents inline that a double-click through one is currently inert until Rigger gains
`${openedFile}`-style handling, a known separate gap), and `shortcut` (`.lnk` creation/removal —
the highest-risk new piece: no COM support exists in `golang.org/x/sys/windows` beyond
`CoInitializeEx`/`GUID`, so this binds `CoCreateInstance` manually and drives the
`IShellLinkW`/`IPersistFile` vtables directly; verified against an independent `WScript.Shell`
COM readback in its own tests, not just "didn't panic"). `internal/console` provides the
`Confirm`/`RetryCancel`/`ReadLine` prompts `cmd/installer`/`cmd/uninstaller` use as their UI
until the WebView2 wizard exists — plain stdin/stdout, fully unit-testable with a fake stdin.

### Tiered feedback logging (§19, `internal/applog`)

Rigger reports what it's doing via `internal/applog.Logger`: `Info`/`Warn` write to
stdout/stderr *and* to a persistent `<DataDir>/rigger.log` (via `layout.RiggerLogPath`), so
there's a durable trace even once `rigger.exe` is eventually built as a windowsgui-subsystem
binary with no console. All methods are nil-receiver-safe — `Info`/`Warn` still print to console
when the `*Logger` is nil (e.g. `Open` failed because DataDir isn't writable), only the file
write is skipped — so `cmd/rigger` never needs a nil-check at each call site. `LogFatal` is
file-only (never console): `main()`'s `uierror.Fatalf` already reports a fatal error to the user,
so reusing `Warn` there would double-print it. A full interactive **doctor/diagnostic mode**
(network checks, registry/JRE validation, log collection, a WebView2 UI) is a documented-but-
deferred requirement — see §19-20b of REQUIREMENTS.md — not yet built.

### Network zones & proxy (§17-18)

Installs also vary by **network zone** (e.g. a corporate intranet, the open internet, a private
extranet) — a build-time axis exactly like Environment, since the manifest server reachable
differs by zone. Unlike `Environment`, `manifest.NetworkZone` is deliberately **not** a closed
enum — it's a validated free-form string, because zone names describe one deployment's specific
network topology (not a generic release stage every app shares), matching how `ProtocolScheme`
is already handled. `appconfig.EnvironmentConfig` holds a `Zones map[NetworkZone]ZoneConfig`, one
manifest per (Environment, Zone) pair.

Each install resolves exactly **one** zone + **one** proxy at install time (auto-detected via
`internal/proxydetect`, operator-overridable), stored as new `Software\<AppId>` registry values
(`NetworkZone` required, `ProxyHost`/`ProxyPort` optional — empty means direct connection).
Rigger never re-detects; it only reads these and (a) passes them to the launched JVM via new
`${networkZone}`/`${proxyHost}`/`${proxyPort}` placeholders (dropped when no proxy is
configured, extending the `${uri.X}`-drop rule), and (b) routes its own manifest-refresh fetch
through `proxydetect.Client(...)` instead of a bare `http.Client` — a real gap fixed while
implementing this, since the JVM-placeholder relay alone doesn't help Rigger's own network
calls. The zone returned on a protocol-handler auth response (§11) is compared against the
installed `NetworkZone` only as a log-and-continue consistency check — there is no multi-zone
lookup table; one install always has exactly one zone/proxy pair.

### Remaining empty placeholder packages

Three directories are still empty, reserved for planned work per `docs/REQUIREMENTS.md` — not
accidentally-empty or safe to repurpose without checking intent first: `internal/wizard` (the
future WebView2 UI shell), `internal/signing` (Authenticode — `stagebuild` reads
`AppConfig.Signing` but only warns that it's unimplemented rather than acting on it), and
`internal/riggerupdate`. (`cmd/maintain` was tried and deliberately removed — see above — not
left as a placeholder; that's a different situation from these three.) `internal/uninstallkey` is
also still empty and likely vestigial — its intended purpose (the standard Add/Remove Programs
Uninstall key) turned
out to already be fully covered by `internal/winreg.WriteUninstallValues`/`DeleteUninstallValues`,
which predates this package and is what `cmd/installer`/`cmd/uninstaller` actually use; check
before assuming it needs filling in.

### Testing end to end via `examples/abc`

- `examples/abc/java` — a minimal Swing fixture app (`Main.java`, built by `build.ps1` into
  `target/app.jar`) matching the fixture manifest's `mainClass`/`classpath`. It reports the
  args/system-properties Rigger resolved, both in a dialog and in `<dataDir>/abc-launch.log`
  (since Rigger launches via `javaw.exe`, which has no console).
- `examples/abc/fakeserver` — a throwaway static file server (`go run ./examples/abc/fakeserver
  -dir <dir>`) standing in for `manifestServerUrl`, logging every request so manifest-refresh
  fetches are visible live.
- `examples/abc/appconfig.json` (a template with `__JRE_VERSION__`/`__JRE_SHA256__` placeholders)
  + `examples/abc/prepare-stagebuild-fixtures.ps1` — zips the local `JAVA_HOME` JDK into a real
  JRE archive, computes its checksum, and writes resolved `appconfig.generated.json`/
  `manifest.generated.json` (gitignored, machine-specific). Feed the generated config to
  `stagebuild -config examples/abc/appconfig.generated.json -env PROD -zone Internet` to build
  and test a real `ABCSetup.exe` end to end (install, registry, shortcuts, protocol handler,
  uninstall — all manually verified working; only the final JVM launch fails, on the known,
  separate "jars aren't bundled" gap, §9/§5).
- `examples/abc/setup-dev-install.ps1` — a lighter-weight alternative that hand-assembles a
  per-user install root without going through `stagebuild`/`cmd/installer` at all, still useful
  for testing `cmd/rigger` changes in isolation. Run it once per machine/JDK change; it prints
  the exact follow-up commands to start `fakeserver` and launch `rigger.exe` both ways.
