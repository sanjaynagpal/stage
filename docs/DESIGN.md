# Stage — Design & Implementation

This document describes how Stage is actually built: the architecture, the data model, the
packages, and the key runtime workflows — as opposed to `docs/REQUIREMENTS.md`, which is the
chronological record of *why* each decision was made. Read `REQUIREMENTS.md` for rationale and
section-numbered history (`§4`, `§13`, etc., referenced throughout this document and the code's
own comments); read this document for the resulting shape of the system and what's actually
implemented today.

## 1. Purpose

Stage is a Windows-only, Go-based toolkit that builds installers for Java desktop applications.
A developer runs `stagebuild` against a per-app JSON config to produce a self-contained
`<App>Setup.exe`, analogous to compiling an Inno Setup script. The installed application is
never launched directly — it's launched by **Rigger** (`rigger.exe`), a small, generic launcher
that reads a JSON **manifest** to know how to invoke the JVM.

`examples/abc/` is a sample/fixture app ("ABC") used to develop and manually test against — not
a real shipping product. Other real apps reuse the same tooling by supplying their own app
config and manifest.

## 2. System Architecture

### 2.1 Binaries

| Binary | Status | Role |
|---|---|---|
| `rigger.exe` (`cmd/rigger`) | **Implemented** | Generic, prebuilt-once launcher. Zero per-app compiled state — derives its app identity from its own install folder name. |
| `stagebuild` (`cmd/stagebuild`) | **Implemented** | Per-app build tool; consumes an `appconfig.AppConfig` and really compiles `cmd/installer` fresh per (Environment, NetworkZone) pair, with that build's payload embedded via `go:embed`. |
| the generated installer (`cmd/installer`) | **Implemented** (polished terminal UI by default, `-gui` for a browser wizard — §23) | Runs the full §16 flow — prerequisite checks, extraction, registry writes, protocol-handler/file-association/shortcut registration. |
| the uninstaller (`cmd/uninstaller`, `unins.exe`) | **Implemented** (minimal console UI) | Self-copy-and-relaunch teardown: removes files, registry keys, protocol handler, file associations, shortcuts. |

Stage ships exactly two binaries per install: `rigger.exe` and `unins.exe`. There is no
`cmd/maintain` — on-demand JRE provisioning (Dynamic package mode, §13-14) runs in-process inside
`rigger.exe` itself; see §2.9c for why, and for the (unimplemented) `rigger.exe` self-update
question.

### 2.2 Why Rigger has zero per-app compiled state

The same `rigger.exe` binary is shipped by every app Stage builds. It figures out which app it
belongs to purely from where it's installed:

```
AppIDFromExePath(exePath) == filepath.Base(filepath.Dir(exePath))
```

i.e. the root install folder *is* named after the app (`C:\...\ABC\rigger.exe` → app id `ABC`),
so Rigger never needs a compiled-in identity. Everything else it needs — install scope, package
mode, manifest server URL, network zone, proxy, data directory — is read from the registry key
`Software\<AppId>` at every launch (`internal/winreg.ReadAppValues`).

### 2.3 The registry is the source of truth Rigger reads, not re-derives

Per `REQUIREMENTS.md` §4, Rigger's own logic stays deliberately minimal: it reads a handful of
registry values and acts, rather than re-deriving install conventions, walking up from its own
exe path, or guessing whether it's allowed to self-update. `internal/layout` centralizes every
filesystem path convention (root dir, JRE dir, manifest path, log path, shortcut dirs, icon path)
so no other package hardcodes them.

Deliberately **not** in the registry: which JRE/app-version folders exist on disk. Those are read
directly off the filesystem at launch time, so there's a single source of truth and no risk of
registry drift.

### 2.4 Directory layout (on disk)

```
<root>/                          %ProgramFiles%\<AppId> (all-users) or %LocalAppData%\<AppId> (per-user)
├── rigger.exe
├── manifest.json                local cached copy, refreshed from ManifestServerUrl
├── app.ico
├── jre/
│   └── <version>/                 e.g. 25.0.1 — bin/javaw.exe lives directly at this root (no vendor wrapper dir)
├── <version>/                    e.g. 1.0.0 — app.jar, lib/*.jar
└── ...                            up to 2 JRE versions and N app-version folders retained (§3)

<DataDir>/                       %AppData%\<AppId> — separate, always user-writable even for an all-users install
└── rigger.log                    Rigger's own tiered feedback log (§2.8 below)
```

### 2.5 Registry layout

`Software\<AppId>` (under `HKLM` for an all-users install, `HKCU` for per-user — the hive itself
encodes scope, and `InstallScope` is *also* stored explicitly so Rigger never infers scope from
which hive answered):

| Value | Type | Meaning |
|---|---|---|
| `InstallScope` | `PerUser` \| `AllUsers` | Drives every scope-dependent decision (§2). |
| `PackageMode` | `Static` \| `Dynamic` | Whether `rigger.exe`/JRE self-update is allowed (§13-14). |
| `AppId` | string | Canonical app id. |
| `InstallDir` | path | Root app folder (belt-and-suspenders alongside deriving it from the exe path). |
| `DataDir` | path | Per-app, user-writable data directory (§9b). |
| `ManifestServerUrl` | URL | Bootstrap manifest location; resilience fallback if the local cache is missing/corrupt. |
| `ProtocolScheme` | string | The registered custom URI scheme (§11), e.g. `acme-abc`. |
| `DisplayVersion` | string | Currently-installed app version, for diagnostics. |
| `NetworkZone` | string, **required** | Build-time network zone this install targets (§17-18) — e.g. `Internet`, `Radianz`. |
| `ProxyHost` / `ProxyPort` | string, optional | Proxy resolved at install time (auto-detected, operator-overridable); empty means direct connection (§17-18). |

Plus the standard Add/Remove Programs `Uninstall` key
(`internal/winreg.WriteUninstallValues`/`UninstallValues`), a separate key per Windows
convention even though a couple of values overlap in content with `Software\<AppId>`.

### 2.6 The manifest — what tells Rigger how to launch the app

`internal/manifest.Manifest`, JSON, cached locally and refreshed from `ManifestServerUrl` at
every launch (best-effort — see §2.10):

```jsonc
{
  "appId": "ABC", "appName": "ABC", "version": "1.0.0",
  "environment": "PROD",                    // DEV | TEST | PROD, baked in at build time
  "manifestServerUrl": "https://.../manifest.json",
  "artifactSha256": "...",                  // verifies the on-demand app-jars archive (§2.9b)
  "runtime": { "javaVersion": "25.0.1", "path": "jre/25.0.1", "sha256": "..." },
  "classpath": ["1.0.0/app.jar"],            // always relative to the install root
  "mainClass": "com.example.abc.Main",
  "jvmOptions": ["-Dabc.env=${environment}", "-Dabc.token=${uri.token}"],
  "arguments": ["--install-dir", "${installDir}"],
  "protocolParams": ["token"],               // allowlist of URI query params usable as ${uri.X}
  "shortcut": { "name": "ABC", "description": "Launches ABC", "startMenu": true, "desktop": true }
}
```

Built-in `${...}` placeholders, substituted by `Manifest.Resolve` into `jvmOptions`/`arguments`:

| Placeholder | Source | Drop-if-empty? |
|---|---|---|
| `${installDir}` | the resolved root path | no |
| `${dataDir}` | registry `DataDir` | no |
| `${environment}` | the manifest's own `environment` field | no |
| `${networkZone}` | registry `NetworkZone` | no (always set — required registry value) |
| `${proxyHost}` / `${proxyPort}` | registry `ProxyHost`/`ProxyPort` | **yes** — the whole token is dropped, not empty-substituted, when no proxy is configured |
| `${uri.<name>}` | a protocol-handler invocation's query params, only for `<name>` declared in `protocolParams` | **yes** — dropped when the param wasn't present on this invocation |

The "drop, don't empty-substitute" rule matters: it's what keeps a direct-connection or
shortcut-launched (non-authenticated) run from producing a dangling `-Dfoo=` flag on the JVM
command line. `Manifest.Validate()` enforces at parse time that every `${...}` used is either a
known built-in or a `${uri.X}` with `X` declared in `protocolParams` — a build-time safety net,
not just a runtime concern.

### 2.7 Build-time config — what produces a manifest and an installer

`internal/appconfig.AppConfig` is `stagebuild`'s input schema: app identity, branding
(icon/license/publisher), the protocol scheme, optional file associations, the initial bundled
JRE spec, output exe name, and optional Authenticode signing config (read but not acted on —
`internal/signing` stays an unimplemented placeholder; `stagebuild` prints a warning rather than
silently ignoring it if `signing` is set).

Manifests are organized per **(Environment × NetworkZone)** pair — both are build-time axes, not
switchable post-install:

```go
Environments map[manifest.Environment]EnvironmentConfig
// EnvironmentConfig{ Zones map[manifest.NetworkZone]ZoneConfig }
// ZoneConfig{ ManifestPath, ManifestServerURL string }
```

Unlike `Environment` (a closed `DEV`/`TEST`/`PROD` enum — a release stage every app shares),
`NetworkZone` is a **validated free-form string**, not an enum: zone names describe one
deployment's specific network topology (a corporate intranet, an extranet like Radianz), which
varies per company, the same way `ProtocolScheme` is already free-form rather than fixed.

### 2.8 Tiered feedback — how Rigger reports what it's doing

`internal/applog.Logger` (added per the requirement that "when Rigger runs it should inform what
is being done"): `Info`/`Warn` write to stdout/stderr *and* durably append to
`<DataDir>/rigger.log` (`internal/layout.RiggerLogPath`), so there's always a trace even once
`rigger.exe` is eventually built as a windowsgui-subsystem binary with no console attached
(`internal/uierror`'s documented end state). All methods are safe to call on a nil `*Logger` —
console output still happens even if the log file couldn't be opened; only the file write is
skipped — so `cmd/rigger` never needs a nil-check at each call site. `LogFatal` is deliberately
**file-only** (never console): `main()`'s `uierror.Fatalf` already reports a fatal error to the
user, so reusing `Warn` there would double-print it.

A much larger interactive **doctor/diagnostic mode** (network reachability checks, registry/JRE
integrity validation, log collection, a `mailto:`-based "contact support" action, a UI for
anything slow) is a documented-but-deferred requirement — see `REQUIREMENTS.md` §19-20b — not
built yet. It was originally planned to reuse an embedded WebView2 shell (§11b); that approach
was abandoned (§23, superseding §11b) in favor of `internal/tui`/`internal/wizard`, so any future
doctor-mode work should target those instead, re-evaluating reuse feasibility at that time.

### 2.9 stagebuild → installer → uninstaller pipeline

`stagebuild -config <appconfig.json> -env <DEV|TEST|PROD> -zone <name>` produces one
self-contained `<OutputName>.exe` (in `dist/`), matching §9's "analogous to compiling an Inno
Setup script" framing — a real `go build` per app build, not a runtime append-bytes-to-a-stub
approach:

1. Loads and validates the app config, resolves the chosen `(Environment, NetworkZone)` pair's
   `ZoneConfig`, loads that zone's manifest and cross-checks its `manifestServerUrl` matches.
2. Populates `cmd/installer/payload/` (gitignored except a committed `PLACEHOLDER.txt` — needed
   because `//go:embed` fails to compile against a missing or empty directory) with:
   `rigger.exe`/`unins.exe` (both freshly `go build`-compiled), the JRE archive copied
   **verbatim** (not extracted — extraction and SHA-256 verification happen at install time, as
   defense-in-depth), the resolved manifest, a copy of the app config, the icon/license, and a
   `build.json` (`internal/payload.BuildMeta`) recording which (Environment, NetworkZone) this
   specific build is for — the installer has no other way to know its own zone, since
   `appconfig.json` alone enumerates every zone the app could ever target.
3. Really compiles `cmd/installer` (`//go:embed all:payload`), then wipes the payload directory
   back to just the placeholder on success (left populated for inspection on failure). An
   exclusive lock file guards against two concurrent builds corrupting each other.

`cmd/installer` (console UI, §16): welcome/license → prerequisite checks (disk space sized from
the embedded files, including the JRE zip's *uncompressed* entry sizes; a running-process check
via `internal/procscan` with a Retry/Cancel prompt) → package-mode and proxy choices (`AllUsers`
forces `Static`; `PerUser` defaults to `Dynamic` with a prompt; `internal/proxydetect` result
shown with an accept/override/none prompt) → extraction (`internal/jreprovision.ProvisionLocal`
against a temp copy of the embedded JRE zip — the same function, and the same `MaxRetainedVersions`
eviction, used for both a fresh install and an upgrade-in-place re-run) → registry writes
(`winreg.WriteAppValues`/`WriteUninstallValues`, `internal/protocolhandler.Register` always,
`internal/fileassoc.Register` only if declared) → shortcuts (`internal/shortcut`, raw COM —
`golang.org/x/sys/windows` has no `IShellLinkW`/`IPersistFile` bindings, so this package binds
`CoCreateInstance` manually and drives the vtables directly) → an `internal/payload.InstallRecord`
receipt (exact protocol scheme, file-association ProgIDs, shortcut paths) written for the
uninstaller → optional immediate launch.

`unins.exe` (console UI): a Windows install directory can't delete its own running exe, so this
is self-copy-and-relaunch — the first invocation copies itself to `%TEMP%` and re-execs with
`--finish-uninstall <root> <appID>`, exiting immediately; the second invocation (running from
outside `root`) reads the `InstallRecord` to know exactly what to unregister, removes shortcuts/
protocol handler/file associations/registry keys, then `os.RemoveAll(root)` with a brief retry
loop for the just-exited-process unlock race, finally scheduling its own `%TEMP%` copy for
delayed deletion (`MOVEFILE_DELAY_UNTIL_REBOOT` — the *only* thing left behind, since it can't
delete itself while running).

Per §9, Stage never bundles app jars — the installer lays down `rigger.exe`, a JRE, and the
manifest only. `internal/jarprovision` (§2.9b) is what used to be the one missing piece here:
before it existed, a real install's "Launch now" extracted/registered everything correctly but
the JVM launch itself failed (empty classpath). That gap is now closed.

### 2.9b On-demand jar delivery (`internal/jarprovision`)

Since jars are never bundled, Rigger fetches them itself. In `cmd/rigger`'s `run()` this happens
right after the JRE-directory check and before classpath resolution: if
`layout.VersionDir(root, m.Version)` doesn't exist on disk, `jarprovision.Provision` downloads
`m.ArtifactDownloadURL()` (the manifest's own final path segment replaced with
`artifacts/<version>.zip`, the app-level analogue of `Manifest.DownloadURL()`'s JRE convention),
verifies it against `m.ArtifactSHA256`, and extracts it into that version directory — reusing
`internal/jreprovision.DownloadVerified` directly rather than duplicating the download/checksum
logic ("shared provisioning, not duplicated logic," extended from JRE acquisition to jar
acquisition). Two differences from the JRE path matter:

- **Not gated on `PackageMode`.** Rigger/JRE binary self-update is the part `PackageMode`
  governs (§13-14); jars/manifest polling is the separate, always-on channel regardless of
  package mode (§9b/§13-14) — so this fetch runs unconditionally when the version directory is
  missing, on both `Static` and `Dynamic` installs.
- **Proxy-aware.** Unlike `jreprovision.Provision`'s bare `http.Client` (fine for the installer's
  one-time local extraction), this fetch is a live network call from an ordinary user session, so
  it's routed through the same `proxydetect`-aware client `cmd/rigger` already built for the
  manifest refresh (§17-18) — `internal/jreprovision.DownloadVerified` takes an `*http.Client`
  parameter specifically so callers can supply one.

Eviction of stale version folders mirrors the JRE side (`MaxRetainedVersions=2`, keep-newest by
modification time) but can't reuse the exact same directory scan: `jre/`'s directory holds only
version subdirectories, while the install root also holds `rigger.exe`, `unins.exe`,
`manifest.json`, and friends alongside version folders. `internal/jreprovision.EvictOldest` was
generalized to take an `include(name string) bool` filter for this reason —
`internal/layout.IsVersionDir` (an exclusion list of Stage's own fixed root-level names) is that
filter for `jarprovision`, while `jreprovision`'s own JRE-side eviction passes an
always-true filter.

### 2.9c On-demand JRE provisioning (in-process, no companion exe)

§13 originally called for a separate "companion maintenance executable" (`maintain.exe`) for this.
That's been superseded — see `docs/REQUIREMENTS.md` §22 (Round 11) — because every additional
shipped binary is an additional thing that needs its own Authenticode signature and its own
AV/SmartScreen reputation, and the constraint that motivated a *separate process* (a running
program generally can't overwrite its own executable file) doesn't actually apply to JRE
provisioning: fetching a JRE into `jre/<version>` never touches `rigger.exe`'s own file, so there's
nothing stopping `rigger.exe` from doing it in-process, in the same run.

In `cmd/rigger`'s `run()`, the former `TODO(Phase 8)` stub at the JRE-directory check now reads:

1. If `appValues.PackageMode != winreg.ModeDynamic`, fail immediately with a clear error, exactly
   as before — Static installs never self-update the runtime.
2. Otherwise, build a second, longer-timeout proxy-aware client (`fetchClient`, 5 minutes, vs. the
   10-second `manifestClient` used for the per-launch manifest poll — a JRE archive can
   legitimately take a while) and call `internal/jreprovision.Provision(fetchClient, root,
   m.DownloadURL(), m.Runtime.SHA256, jreDir)` directly — the exact routine the installer's
   `ProvisionLocal` shares the download-verify-extract-evict core with (§12), just reached via a
   network fetch instead of a local temp file. The same `fetchClient` is also now used for
   `jarprovision.Provision`'s jar fetch (§2.9b), which had been reusing the short manifest-poll
   client — a latent timeout risk for a real jar archive on a slow link, fixed here in passing.
3. On success, log and continue with the now-present `jreDir`; on failure (checksum mismatch,
   download error), `run()` returns an error that surfaces as a single clear fatal report via
   `main()`'s `uierror.Fatalf`, never launching against a mismatched/missing runtime (§12-14's
   confirmed failure behavior).

**`rigger.exe` binary self-update** (the other half of §13's original "JRE/rigger self-update"
companion-exe framing) is still not implemented, and — per §22 — won't need a companion exe either
when it is: it would reuse the exact self-copy-to-`%TEMP%`-and-relaunch pattern `unins.exe`
already uses on itself (copy `rigger.exe` to `%TEMP%`, run that copy with a flag, have it
overwrite the real `rigger.exe` in the install root, then relaunch) — no new binary, no new
signing surface. It's still unimplemented today for an unrelated reason: there's no
manifest/appconfig field yet to declare a rigger.exe version + download URL + checksum (analogous
to `RuntimeSpec`), which is a real schema decision, not something "depends on nothing new."

### 2.9d `cmd/installer`'s two UIs (`internal/tui`, `internal/wizard`)

§11b originally called for an embedded WebView2 control; that was implemented far enough to hit
real problems (Go-implemented COM callbacks, a hand-rolled Win32 message loop, an unsourceable
`WebView2Loader.dll`) and abandoned. §23 records the full reasoning; this section covers the
resulting architecture. `cmd/installer` now has an `installerUI` interface
(`Confirm`/`RetryCancel`/`ReadLine`/`Notify`) that `run()`'s install logic is written against,
with two implementations selected by a `-gui` flag (default: terminal):

- **`internal/tui`** — an Elm-architecture Bubble Tea program (`github.com/charmbracelet/
  bubbletea` + `lipgloss` — this repo's first third-party dependency beyond `golang.org/x/sys`).
  A `model` holds which screen is active; `Update` handles both `installerUI` request messages
  (sent via `program.Send`, e.g. a `confirmRequest`) and `tea.KeyMsg` key presses, delivering the
  operator's answer on a per-call response channel; `View` renders it styled via `lipgloss`
  (a rounded-border box, colored title, a log of `Notify` lines). Since `tea.Program.Run()` blocks
  until quit, it runs on its own goroutine; `installerUI` methods block the calling (installer)
  goroutine on the response channel. Ctrl+C exits the whole process — the same effective behavior
  a plain console prompt already had, not a regression.
- **`internal/wizard`** — a `net/http.Server` on an ephemeral loopback port, opened via
  `msedge.exe --app=...` for a chrome-less window (falling back to the OS default browser).
  Every route requires a random per-session token embedded in the URL. Screens are Go
  `html/template` fragments; HTMX (`go:embed`ded JS, plain source — no binary/supply-chain
  concern) drives partial-page swaps on form submission, an every-2s heartbeat POST (missing
  heartbeats abort the process, the browser-UI analogue of `internal/tui`'s Ctrl+C-quits
  behavior), and an every-1s poll of the current screen. `installerUI` methods block on a shared
  `state` struct's `sync.Cond`: after delivering an answer, the UI transitions to an idle/log
  screen immediately (`setIdle`) rather than waiting for the *next* interactive prompt, which
  might be seconds away (e.g. across the extraction phase) — the idle screen's own polling is
  what then surfaces progress and, eventually, the next prompt. (`Notify` deliberately does
  *not* wake a waiter for this reason — an earlier version did, and a `Notify` call landing
  between two prompts caused the already-answered prompt to be redisplayed; caught by manual
  end-to-end testing and covered by a regression test,
  `TestNotifyBetweenPromptsDoesNotStallOrCorruptNextScreen`.)

Both are genuinely unit-testable — `internal/tui` by feeding messages into `Update` and asserting
on the resulting model, `internal/wizard` by driving its handlers with `httptest` — unlike the
abandoned WebView2 approach, which had no path to automated testing at all.

### 2.10 Update model

Two independent channels, regardless of package mode:

- **Jars/manifest**: Rigger polls `ManifestServerUrl` on every launch. A successful fetch
  replaces the local cache; a failed fetch (offline, server down, invalid content) silently
  falls back to the last-good cache — connectivity is only required to *check* for updates,
  never to launch (§9b). This fetch is routed through `internal/proxydetect.Client`, built from
  the registry's resolved proxy, not the process environment (§17-18) — a real gap fixed
  alongside the network-zone work, since the JVM-placeholder relay alone didn't help Rigger's
  *own* network calls. When the manifest's `version` names a directory not yet present on disk
  (first launch, or a version bump since the last poll), `internal/jarprovision` fetches and
  unpacks that version's jars through the same proxy-aware client before launch proceeds (§2.9b)
  — this is the same always-on channel, not a separate one.
- **`rigger.exe`/JRE binaries**: governed by `PackageMode`. `AllUsers` installs are always
  forced `Static` (Program Files isn't reliably writable by an ordinary later launch);
  `PerUser` installs default to `Dynamic` and can self-update the JRE on demand, in-process, when
  a manifest requests a version not on disk (§2.9c) — failing clearly if provisioning doesn't
  succeed, rather than launching a mismatched runtime. `rigger.exe` binary self-update itself is
  not yet implemented (§2.9c).

## 3. Package Reference (`internal/*`)

| Package | Purpose | Notable design decision |
|---|---|---|
| `layout` | Centralizes every filesystem path convention (root/JRE/version/manifest/log/icon/shortcut dirs). | Single source of truth so no other package hardcodes a path. |
| `manifest` | Defines and validates the launch manifest; substitutes `${...}` placeholders. | `Environment` is a closed enum; `NetworkZone` deliberately isn't (§2.7). Drop-not-empty-substitute rule for optional placeholders. |
| `appconfig` | `stagebuild`'s input schema; validates an app's full build config. | Zones nested under Environment (`EnvironmentConfig.Zones`), not a flat/composite key — every (env, zone) pair needs its own manifest server. |
| `winreg` | Reads/writes `Software\<AppId>` and the standard Uninstall key. | Tries `HKCU` then `HKLM` on read, but trusts the key's own `InstallScope` value rather than inferring scope from which hive answered. |
| `applog` | Tiered console+log-file feedback (§2.8). | Nil-receiver-safe; `LogFatal` is file-only to avoid double-printing with `uierror`. |
| `proxydetect` | WinHTTP-based, PAC-aware proxy auto-detection + a proxy-routed `http.Client`. | `golang.org/x/sys/windows` has no WinHTTP bindings — binds `winhttp.dll` directly via `LazyDLL`/`LazyProc`. Never consults `HTTP_PROXY` env vars — the registry's resolved value is the only source of truth. |
| `jreprovision` | Downloads, SHA-256-verifies, and unpacks a JRE archive; evicts oldest past `MaxRetainedVersions=2`. | Shared by the installer (`ProvisionLocal`, a local archive) and `cmd/rigger` itself (`Provision`, an in-process network fetch through a caller-supplied `*http.Client`) — "JRE acquisition is not duplicated logic in Rigger" (§12) means the routine is shared, not that it must run out-of-process. Its `DownloadVerified`/`EvictOldest` primitives are exported and reused by `jarprovision` rather than duplicated. |
| `jarprovision` | The app-jars analogue of `jreprovision`: fetches/verifies/unpacks the version directory `cmd/rigger` finds missing on disk, evicting old versions past `MaxRetainedVersions=2`. | Always runs regardless of `PackageMode` (unlike JRE/rigger.exe self-update) — jars are the always-on channel (§9b/§13-14). Eviction filters on `layout.IsVersionDir` since the install root, unlike `jre/`, also holds Rigger's own fixed files. |
| `javainvoke` | Resolves the classpath (including glob entries) and execs `javaw.exe` as a detached process. | Rigger execs and exits — it does not supervise the JVM. |
| `uriparse` | Parses a protocol-handler invocation URI and extracts query params. | Matches against the app's own registered scheme specifically (not "does this look URI-shaped") — a bare Windows drive letter parses as scheme `c` otherwise. |
| `archiveutil` | Extracts a zip archive, rejecting zip-slip path traversal. | Archives are extracted flat/verbatim — no auto-detected vendor wrapper stripping (ambiguous to do safely). |
| `elevate` | Reports whether the current process is elevated. | Drives install scope: elevated → all-users, otherwise → per-user (§2). |
| `diskspace` | Free disk space at a (possibly not-yet-existing) target path. | Walks up to the nearest existing ancestor directory to query. |
| `procscan` | Enumerates running processes under a given root (Toolhelp32 snapshot). | Used by the installer's and uninstaller's prerequisite checks to detect a running `rigger.exe`/`java.exe` locking the target. |
| `uierror` | Native `MessageBox` fallback for windowsgui-subsystem binaries with no console. | Also writes to stderr — harmless no-op when no console is attached, useful in dev. |
| `payload` | Schema shared between `stagebuild` (writer) and `cmd/installer`/`cmd/uninstaller` (readers): payload filenames, `BuildMeta`, `InstallRecord`. | Keeps the three binaries from hardcoding divergent filenames or duplicating JSON shapes. |
| `protocolhandler` | Registers/unregisters the custom URI scheme (§11). | Writes the conventional `"URL Protocol"` marker too, not just `shell\open\command`. Leaf-first delete on unregister (`registry.DeleteKey` requires an empty key). |
| `fileassoc` | Registers/unregisters optional file-type associations (§16 step 3). | Documents inline that double-clicking an associated file is currently inert — Rigger has no `${openedFile}`-style handling yet, a known, separate gap. |
| `shortcut` | Creates/removes `.lnk` files via raw COM (`IShellLinkW`/`IPersistFile`). | `golang.org/x/sys/windows` has no COM bindings beyond `CoInitializeEx`/`GUID` — binds `CoCreateInstance` manually and drives vtables directly. Verified against an independent `WScript.Shell` readback, not just "didn't crash." |
| `console` | Minimal `Confirm`/`RetryCancel`/`ReadLine` prompts — `cmd/uninstaller`'s UI (§2.9d: `cmd/installer` now uses `internal/tui`/`internal/wizard` instead). | Fully unit-testable by feeding a fake stdin; no real console needed. |
| `tui` | `cmd/installer`'s default UI: a Bubble Tea/Lipgloss terminal wizard implementing `installerUI` (§2.9d, §23). | This repo's first third-party Go dependency beyond `golang.org/x/sys`, deliberately accepted since the terminal path is now permanent, not a stand-in. Tested by feeding messages into `Update`. |
| `wizard` | `cmd/installer`'s `-gui` UI: a local `net/http` server + HTMX opened as a browser "app window," implementing `installerUI` (§2.9d, §23). | No COM, no message loop, no native binary dependency — HTMX is checked-in JS source. Tested with `httptest`. |

`internal/riggerupdate` and `internal/signing` remain empty placeholder packages (reserved for
planned work per `REQUIREMENTS.md`) — not accidentally empty, not safe to repurpose without
checking intent first. `internal/uninstallkey` is also still empty and likely
vestigial: its intended purpose (the standard Uninstall registry key) turned out to already be
fully covered by `internal/winreg.WriteUninstallValues`/`DeleteUninstallValues`, predating this
package, which `cmd/installer`/`cmd/uninstaller` actually use.

## 4. Key Runtime Workflow — `rigger.exe`'s launch sequence

`cmd/rigger/main.go`'s `run()`, in order:

1. Resolve own exe path → derive `appID`/`root` (`layout.AppIDFromExePath`/`RootDirFromExePath`).
2. `winreg.ReadAppValues(appID)` — any failure here means a corrupt/missing install; reported
   via `uierror.Fatalf`, and (since `DataDir` isn't known yet) this is the one failure mode that
   can never be file-logged.
3. Open `applog` at `layout.RiggerLogPath(appValues.DataDir)` — best-effort; console output
   continues even if this fails.
4. Determine invocation kind: `argv[1]` present and its scheme matches `ProtocolScheme` →
   protocol-handler invocation (`uriparse.Parse`); otherwise a plain shortcut launch.
5. If the invocation carries a `networkZone` query param that doesn't match the registry's
   `NetworkZone`, log a warning (non-fatal consistency check, not a lookup — §17-18 Round 8 Q1).
6. Build two proxy-routed `http.Client`s (`proxydetect.Client`) from the registry's
   `ProxyHost`/`ProxyPort`: a short-timeout one for the manifest poll, a long-timeout one for the
   on-demand archive fetches in steps 8-9.
7. `loadManifest`: load the local cache, then best-effort refresh from `ManifestServerUrl`
   (falling back silently to cache on failure — §2.10).
8. Confirm the manifest's required JRE directory exists on disk; if not and `PackageMode` is
   `Dynamic`, fetch/verify/extract it in-process via `jreprovision.Provision` (§2.9c), failing
   clearly if `PackageMode` is `Static` or provisioning doesn't succeed.
9. Confirm the manifest's version directory exists on disk; if not, fetch and unpack it via
   `jarprovision.Provision` (§2.9b) before continuing — this fetch is unconditional, not gated on
   `PackageMode`.
10. Resolve the classpath (`javainvoke.ResolveClasspath`, handling `*` globs) and the
   `${...}` placeholders (`Manifest.Resolve`).
11. `javainvoke.Launch` — exec `javaw.exe` detached, and exit.

Every step from (3) onward logs its outcome via `applog`; any error returned from `run()` is
also recorded via `logger.LogFatal` before `main()` reports it through `uierror.Fatalf`.

## 5. Implementation Status

| Area | Status |
|---|---|
| `cmd/rigger` — full launch flow, protocol-handler path, zone/proxy resolution, tiered logging | **Implemented, unit-tested, manually verified end-to-end** |
| `cmd/stagebuild` — payload population, real `go build` invocation, output to `dist/` | **Implemented, manually verified end-to-end** |
| `cmd/installer` — full §16 flow, terminal UI by default, `-gui` for a browser wizard (§2.9d, §23) | **Implemented, manually verified end-to-end** (real install, real registry/shortcuts/protocol handler, both UIs) |
| `cmd/uninstaller` — self-copy-and-relaunch teardown | **Implemented, manually verified end-to-end** (confirmed complete removal) |
| `internal/*` packages listed in §3 (all but the two remaining placeholders) | **Implemented, unit-tested** |
| `internal/jreprovision`'s on-demand invocation from a *running* Rigger (Dynamic mode), in-process (§2.9c) | **Implemented, manually verified end-to-end** — `cmd/rigger` calls `jreprovision.Provision` directly when the required JRE is missing and `PackageMode` is `Dynamic`. |
| `rigger.exe` binary self-update | Not implemented — no manifest/appconfig field exists yet to declare a rigger.exe version/download location (§2.9c) |
| `internal/tui` / `internal/wizard` (`cmd/installer`'s two UIs, §2.9d) | **Implemented, unit-tested, manually verified end-to-end** — supersedes the originally planned embedded-WebView2 shell (§23) |
| Doctor / diagnostic mode | Requirement documented (`REQUIREMENTS.md` §19-20b); not implemented |
| Jar delivery via Rigger fetching from `ManifestServerUrl` (§9, `internal/jarprovision`) | **Implemented, unit-tested** — `cmd/rigger` fetches/unpacks a missing version directory before launch, verified by checksum, evicting old versions past `MaxRetainedVersions=2`. |

## 6. Testing & Verification

- **Unit tests**: plain `testing`, no framework, following the convention already set across
  `internal/*` — table-driven where useful, `t.TempDir()` for filesystem tests, and a live
  (non-mocked) test against the real Windows API where one exists and is cheap to call
  (`diskspace`, `procscan`, `proxydetect` — asserting structural sanity/logging the result rather
  than a fixed expected value, since actual machine state varies).
- **`examples/abc`** is the fixture app used for manual end-to-end testing:
  - `examples/abc/java` — a minimal Swing fixture app reporting the args/system-properties
    Rigger resolved, both via dialog and its own log file.
  - `examples/abc/fakeserver` — a throwaway static file server standing in for
    `ManifestServerUrl`, logging every request.
  - `examples/abc/appconfig.json` (template) + `examples/abc/prepare-stagebuild-fixtures.ps1` —
    zips the local `JAVA_HOME` JDK into a real JRE archive, computes its checksum, and writes
    placeholder-substituted `appconfig.generated.json`/`manifest.generated.json` (gitignored,
    machine-specific), ready to feed to `stagebuild -config examples/abc/appconfig.generated.json
    -env PROD -zone Internet`.
  - `examples/abc/setup-dev-install.ps1` — a lighter-weight alternative that hand-assembles a
    per-user install root without going through `stagebuild`/`cmd/installer` at all, still
    useful for testing `cmd/rigger` changes in isolation.
  - Every feature built so far (network zones, proxy resolution, tiered logging, and now the full
    `stagebuild`/`installer`/`uninstaller` pipeline including COM-based shortcut creation) has
    been exercised live end to end on a real Windows machine, not just unit-tested.

## 7. Known Gaps & Next Steps

Roughly in dependency order:

1. **Doctor/diagnostic mode** (§19-20b) — has several genuinely open questions (invocation
   trigger, exact network-test semantics, log-collection format) that need resolving before
   implementation; also needs to pick a UI approach now that the originally planned WebView2
   shell it was going to reuse doesn't exist (§23) — `internal/tui`/`internal/wizard` are the
   candidates to re-evaluate.
2. **`rigger.exe` binary self-update** (§13) — needs a new manifest/appconfig field to declare a
   rigger.exe version + download location + checksum (analogous to `RuntimeSpec`), which is a real
   schema decision, not implemented speculatively alongside the JRE-provisioning work (§2.9c).
   When it is built, it won't need a companion exe either — see §2.9c's self-copy-relaunch note.

Jar delivery via Rigger (§9) — previously the top item here — is now implemented
(`internal/jarprovision`, §2.9b); the full `stagebuild` → install → launch pipeline works end to
end with real application code, not just the launcher/runtime/registration plumbing. On-demand JRE
provisioning (§12-14) — implemented in-process inside `cmd/rigger` rather than via a separate
`cmd/maintain` binary (§2.9c, §22). `cmd/installer`'s UI (§11b) — the item before that — is also
now implemented, as a polished terminal wizard by default plus a `-gui` browser wizard, replacing
the originally planned embedded WebView2 control (§2.9d, §23).
