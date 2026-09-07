# Stage — Requirements (Draft v0.1)

Status: **DRAFT — gathering requirements, nothing implemented yet.**

## 1. Purpose

**Stage** is a Windows installer, written in Go, whose job is to "set the stage" on a target
machine for a Java desktop application to be installed and later launched. It must match or
exceed the visual and functional quality of professional installers (Inno Setup / WiX /
InstallShield class).

Stage does not launch the application itself — it lays down everything needed and hands off
launching to a bundled launcher, **Rigger** (`rigger.exe`), which reads an **application
manifest** (JSON) to know how to start the Java app.

Throughout this document the example application being staged is called **ABC**.

## 2. Install Scope & Elevation

| Operator privilege | Install scope | Root location (proposed) | Registry root (proposed) |
|---|---|---|---|
| Elevated (admin) | All users on the machine | `%ProgramFiles%\ABC` (confirmed, §16) | `HKLM\Software\ABC` |
| Standard user (default) | Current user only | `%LocalAppData%\ABC` | `HKCU\Software\ABC` |

- Stage must detect elevation state at launch and choose scope automatically, defaulting to
  per-user when not elevated.
- Whether the operator can *choose* to elevate ("Install for all users" prompting a UAC
  re-launch) from a non-elevated start is TBD (see open questions).

## 3. Directory Layout

Root application folder is named after the app (`ABC`), for both the filesystem and the
registry key:

```
ABC/                            (root app folder — %LocalAppData%\ABC or %ProgramFiles%\ABC)
├── rigger.exe                  (application launcher)
├── jre/
│   ├── 17.0.9+9/                (a specific runtime version)
│   └── 21.0.2+13/               (at most 2 versions retained at any time)
├── 1.4.0/                      (a version folder — application artifacts for that version)
│   ├── app.jar
│   └── lib/*.jar
├── 1.5.0/                      (a newer version, if retained alongside)
└── manifest.json               (local cached copy of the manifest — location TBD)
```

Notes / assumptions to confirm:
- At most **2** JRE versions retained at once; oldest/unused evicted on install of a 3rd.
- Multiple **version** folders (app artifacts) can coexist; retention policy resolved in §21 —
  at most **2** retained at once, mirroring the JRE policy above.
- Java version + location is **always relative to the root app folder** (per requirement),
  e.g. manifest says `jre/21.0.2+13` rather than an absolute path — this makes the whole
  `ABC` folder relocatable/xcopy-safe.

## 4. Registry Layout

Guiding principle (per operator direction): the registry is the **source of truth for
things Rigger would otherwise have to infer or hardcode**, so Rigger's own logic stays
minimal — it reads a few values and acts, rather than re-deriving install conventions,
walking up from its own exe path, or guessing whether it's allowed to self-update.

Root key: `Software\<AppId>` (e.g. `Software\ABC`) under **`HKLM`** for an all-users install
or **`HKCU`** for a per-user install — the hive itself is chosen by scope, but scope is
*also* written explicitly as a value (below) so Rigger never has to infer it from which hive
it happened to find data in.

`Software\ABC` values:

| Value | Example | Purpose |
|---|---|---|
| `InstallScope` | `PerUser` \| `AllUsers` | Drives every other scope-dependent decision Rigger makes (e.g. whether Dynamic mode is even possible, §14) without recomputing it from paths. |
| `PackageMode` | `Static` \| `Dynamic` | Tells Rigger/the maintenance exe whether attempting a `rigger.exe`/JRE self-update is even allowed (§13/§14). `AllUsers` installs always have this set to `Static`. |
| `AppId` | `ABC` | The canonical id, used to build conventional paths (data dir, etc.) and for logging/diagnostics. |
| `InstallDir` | `C:\Users\...\AppData\Local\ABC` | Root app folder — belt-and-suspenders alongside Rigger deriving it from its own exe path; explicit value is what the maintenance exe / uninstaller / repair logic reads to avoid every component doing that derivation itself. |
| `DataDir` | `C:\Users\...\AppData\Roaming\ABC` | Per-app data directory (§9b) — stored explicitly so neither Rigger nor the app has to know/recompute the convention. |
| `ManifestServerUrl` | `https://.../abc/prod/manifest.json` | The **bootstrap** manifest location as of install time. Acts as a resilience fallback: if the locally cached `manifest.json` is ever missing/corrupt, Rigger still has a known-good place to refetch from without any other config. (The live manifest also carries its own `manifestServerUrl` — normally the same value — which is what's used for routine update checks.) |
| `ProtocolScheme` | `acme-abc` | The custom URI scheme registered for this app (§11), recorded for diagnostics/repair/uninstall (removing the `Classes\<scheme>` registration) even though Rigger itself doesn't need to look this up to handle an incoming invocation. |
| `DisplayVersion` | `1.5.0` | Currently-installed app version, for diagnostics/support — the manifest remains the operational source of truth for what to actually launch. |

Deliberately **not** stored in the registry: the list of installed JRE versions, or which
app-version folders exist. Those are read directly off disk (at most 2 of each) so there's
a single source of truth and no risk of the registry drifting out of sync with what's
actually on the filesystem.

Standard **Uninstall** key, so the app appears correctly in "Add or Remove Programs":
`...\Microsoft\Windows\CurrentVersion\Uninstall\ABC` with `DisplayName`, `DisplayVersion`,
`Publisher`, `InstallLocation`, `UninstallString`, `EstimatedSize`, `DisplayIcon` — a
standard, separate key from `Software\ABC` per Windows convention, even though a couple of
values (InstallLocation, DisplayVersion) overlap in content.

## 5. The Manifest (JSON)

Describes everything Rigger needs to launch the app. Draft shape (fields inferred from the
requirements — needs review):

```jsonc
{
  "appId": "ABC",
  "appName": "ABC",
  "version": "1.5.0",
  "environment": "PROD",              // DEV | TEST | PROD
  "manifestServerUrl": "https://.../abc/manifest.json",  // canonical source, for updates
  "runtime": {
    "javaVersion": "21.0.2+13",
    "path": "jre/21.0.2+13"           // relative to root app folder
  },
  "classpath": [
    "1.5.0/app.jar",
    "1.5.0/lib/*.jar"
  ],
  "mainClass": "com.example.abc.Main",
  "jvmOptions": ["-Xmx512m", "-Dabc.env=${environment}"],
  "arguments": [
    "--env", "${environment}",
    "--install-dir", "${installDir}",   // Rigger-injected at runtime
    "--mode", "standard"                // static value
  ],
  "shortcut": {
    "name": "ABC",
    "description": "Launches ABC",
    "startMenu": true,
    "desktop": true
  }
}
```

Open items: full placeholder/injection vocabulary, where the manifest physically lives
(bundled at install time vs. fetched from `manifestServerUrl` at every launch vs. both with
caching + fallback), and per-environment manifest variants (§9).

## 6. Rigger (`rigger.exe`)

- A small native launcher (language TBD — likely also Go, or could be a thin native stub)
  that:
  1. Locates its manifest (local cache, and/or refreshes from `manifestServerUrl`).
  2. Resolves the JRE path relative to the root app folder.
  3. Builds the `java` invocation: classpath, main class, JVM options, arguments — resolving
     static values and injecting runtime values (install dir, environment, user, etc.).
  4. Launches the JVM process and exits (or supervises it — TBD).

## 7. Stage (the installer)

- GUI wizard, visually and functionally on par with professional installers: welcome,
  license/EULA, install location (admin only), progress, finish, shortcut options.
- Bundles and lays down: `rigger.exe`, a JRE, and the manifest JSON (per the prompt). Whether
  application jars/version folders are also bundled in the installer or fetched later by
  Rigger from `manifestServerUrl` is an open, architecture-defining question (§9 Q2).
- Creates Start Menu / Desktop shortcuts using the manifest's `shortcut.description` etc.
- Registers an uninstaller.
- Written in Go (target Go 1.26/1.27 per request — will pin to whatever is the current
  latest stable at implementation time).

## 8. Explicit Requirements Recap (from the original request)

- [x] Windows installer written in Go (1.26/1.27).
- [x] Visually/functionally matches or exceeds professional installers.
- [x] Bundles rigger.exe + a Java runtime + a manifest JSON.
- [x] Elevation-aware install scope: all-users if admin, per-user by default.
- [x] Root folder & registry root named after the app (e.g. `ABC`).
- [x] Root folder holds rigger.exe, a `jre` folder (up to 2 versions over time), and version
      folder(s) with app artifacts (jars).
- [x] Rigger reads the manifest to know: jars, main class, JVM options, arguments (static or
      Rigger-injected).
- [x] Java version/location always relative to root app folder.
- [x] Manifest indicates runtime environment (DEV/TEST/PROD) and the server location where
      the canonical manifest lives.
- [x] Manifest carries the description needed for Desktop/Start Menu shortcuts.

## 9. Decisions (Round 1)

- **Jar delivery**: Application jars are **not** bundled by Stage. The installer only lays
  down `rigger.exe`, a JRE, and the manifest. Rigger fetches jars (and refreshes the
  manifest) from `manifestServerUrl` — first on initial launch, and thereafter as its
  update mechanism. This matches §8's literal requirement (jars weren't listed as part of
  Stage's payload).
- **Packaging model**: Stage is a **per-app build tool** (Go program/toolchain), not a
  single generic runtime installer. A developer runs it at build time against an app-specific
  config (name, license, icon, JRE build, initial manifest) to produce one self-contained
  `ABCSetup.exe` for that app — analogous to compiling an Inno Setup script. Each shipped
  installer is independent; nothing is shared across different apps' installers at runtime.
- **Code signing**: Build pipeline will support Authenticode signing (`signtool`) of both the
  generated installer and `rigger.exe` from the start. Certificate acquisition/storage is a
  later logistics detail, not an architecture blocker.
- **Silent install**: v1 targets the **GUI wizard only**. Silent/unattended CLI flags for
  SCCM/Intune/GPO deployment are explicitly deferred to a later version.

### Working model this implies (confirmed)

- **Two independent update channels**: (1) *Stage-level* updates (new `rigger.exe`, new JRE)
  arrive by the operator re-running a newer Stage-built installer, which performs an
  upgrade-in-place (adds a JRE version alongside the existing one, evicting the oldest past
  2). (2) *App-level* updates (new jars/manifest) arrive via Rigger polling
  `manifestServerUrl`, with no installer involved.
- Since jars aren't bundled, **first launch after install requires network access** to
  `manifestServerUrl` to fetch the initial version's jars — there is no fully-offline first
  run. Confirm this is acceptable.

## 9b. Decisions (Round 2)

- **Environment**: DEV/TEST/PROD is **baked in** at build/install time (a distinct manifest
  per environment); not switchable post-install without reinstalling.
- **Offline fallback**: Rigger falls back to the **last cached** manifest+jars and runs
  normally if `manifestServerUrl` is unreachable; connectivity is only required to *check for
  updates*, never to launch.
- **Upgrade UX**: Re-running a newer Stage-built installer over an existing install performs
  a **silent in-place upgrade** — no Repair/Modify/Uninstall choice screen.
- **Branding**: Icon/logo/product name/publisher are **app-specific inputs** to the Stage
  build tool, embedded into the generated installer, `rigger.exe`, and shortcuts.
- **Data directory**: Stage **creates** a separate per-app, user-writable data directory
  (e.g. `%AppData%\ABC`) distinct from the install root, even for all-users installs.
- **Target platform**: **x64 only, Windows 10+** for v1. ARM64 deferred.
- **Runtime-injected values (v1)**: `${installDir}`, `${environment}`, plus a
  **token/auth launch flow** — see §11.

## 11. Protocol-Handler / Token Launch Flow (new requirement)

The operator described an additional launch path: a web page performs authentication against
a server, passing some request params; on success the server redirects the browser to a
**custom URI scheme registered for app ABC** (e.g. `abc://...`), which the OS routes to
`rigger.exe`. Rigger receives the token and the original request params via that URI and
injects them into the Java app's runtime arguments.

This means, in addition to the plain "launch from Start Menu/Desktop shortcut" path, Rigger
must also support:
- Being registered as the handler for a custom protocol scheme (registry: `HKCR\<scheme>` or
  per-scope `HKCU/HKLM\Software\Classes\<scheme>`, `shell\open\command` → `rigger.exe "%1"`).
  Stage must perform this registration at install time.
- Parsing an invocation URI (`abc://host/path?token=...&param1=...`) passed as `argv[1]`,
  extracting the token and other params.
- Injecting the token/params into JVM system properties and/or program arguments per the
  manifest's placeholder declarations (extending the `${...}` vocabulary from §5/§9b).

## 10. Decisions (Round 3 — protocol launch details)

- **Protocol scheme name**: **separately configurable** per app in its Stage build config
  (not forced to equal the lowercased app id) — e.g. app `ABC` could register scheme
  `acme-abc://`.
- **Single-instance behavior**: **always spawn a fresh JVM** per protocol invocation for v1.
  No IPC-forwarding to an already-running instance; that's out of scope for now.
- **Param injection**: token/params land as **JVM system properties**
  (`-Dabc.token=...`), and only params the **manifest explicitly declares** by name are
  extracted from the invocation URI — the rest of the query string is ignored.
- **Token trust**: **not Rigger's job**. Rigger extracts and passes through declared values;
  validating the token is genuine/unexpired is entirely the Java app's (and its server's)
  responsibility.

## 11b. Decisions (Round 4 — GUI & repo scope)

- **GUI technology**: wizard UI is rendered as **HTML/CSS/JS inside an embedded Microsoft
  Edge WebView2 control**, driven from Go via a local bridge (e.g. `webview/webview_go` or a
  direct WebView2 COM binding). This gives full pixel-level design control for a modern,
  polished look, at the cost of a WebView2 runtime dependency (present by default on Windows
  10 1803+/11, acceptable given the Windows 10+ target).
- **Repository scope**: this repo (`stage`) is the **reusable Stage build tool + Rigger
  launcher source**. `ABC` is a sample/fixture app config used to develop and test against
  during this build — not a real shipping product. Other real apps would reuse this same
  tooling later by supplying their own app config.

## 12. On-Demand JRE Provisioning (new requirement)

If a (possibly freshly-fetched) manifest declares a `runtime.javaVersion` that does **not**
exist under the root app folder's `jre/` directory, Rigger cannot just launch — it must
arrange for that JRE to be fetched and installed before proceeding. Per the operator, Rigger
"asks Stage to get that and install it" — i.e. **JRE acquisition is not duplicated logic in
Rigger**; it calls back into the same provisioning capability Stage's installer uses at
initial-install time.

This amends the §9b "two independent update channels" model: the channels are independent
for jars/manifest (pure Rigger↔server), but **share a JRE-provisioning path** for runtimes.

Concretely this needs:
- A **JRE provisioning routine**, implemented once (shared package/component used by both
  the generated installer and by whatever Rigger invokes), that can fetch a specific Java
  version from a source, verify it, unpack it under `jre/<version>` in the root app folder,
  and apply the existing max-2-versions eviction policy.
- A way for **Rigger to invoke this routine post-install**, when running standalone (no
  installer wizard in progress). Candidates: (a) Rigger calls the shared provisioning
  package in-process/headless itself; (b) Rigger shells out to a small companion
  maintenance executable (built from the same Stage codebase) left behind after install
  specifically for this purpose; (c) Rigger re-invokes a cached copy of the original
  installer exe in a hidden "add-component" mode.
- Some **user-visible feedback** while this happens (a JRE download can be tens of MB and
  take real time) — likely reusing the same WebView2-based UI shell for visual consistency
  with the installer, shown as a lightweight "preparing…" screen rather than the full wizard.
- A **source of truth for JRE downloads**: given an arbitrary required version string, where
  does the archive + checksum come from? (a public distributor's API like Eclipse
  Temurin/Adoptium, a specific vendor's fixed download URLs, or a company-run artifact
  server/catalog declared alongside the manifest). Whatever it is, the download should be
  integrity-checked (checksum/signature) before being unpacked into the app folder, since
  it's executable code that will subsequently be run.
- **Elevation mismatch risk**: for an **all-users** install (root under `%ProgramFiles%`,
  requires admin to write), the *app itself* is normally launched by an ordinary (possibly
  non-admin) user — who has no rights to write a new JRE into that location and no way to
  silently elevate without a UAC prompt (and standard users typically can't approve one at
  all). This is unresolved — see open questions below.
- **Failure/fallback behavior**: if the required JRE can't be fetched (offline, source
  unreachable, integrity check fails), should Rigger refuse to launch with a clear error
  (recommended — running app code against the wrong major Java version is likely to crash
  anyway, unlike the jars/manifest case where an old-but-working cached copy is safe to fall
  back to), or attempt something else?

## 13. Decisions (Round 5 — JRE provisioning)

- **Static vs Dynamic package mode (new concept)**: at install time the operator chooses
  whether the installed package is **Static** or **Dynamic**:
  - **Static**: `rigger.exe` and the JRE(s) are fixed at install time and can **never** be
    updated in place. Any change to either requires the operator to re-install/upgrade via a
    newer Stage-built installer.
  - **Dynamic**: the app is installed to a **write-privileged location**, and Rigger (via the
    shared Stage provisioning machinery) can check the remote server for a newer
    `rigger.exe` and can install a new JRE on demand when the manifest requests one it
    doesn't have — without a fresh installer run.
  - This directly resolves the elevation-mismatch question from §12: an all-users
    (`%ProgramFiles%`, admin-only-write) install is inherently unable to support Dynamic mode
    for ordinary (non-admin) subsequent launches, while per-user (`%LocalAppData%`) installs
    are always self-writable and can support either mode. Exact wiring between the
    all-users/per-user choice and the Static/Dynamic choice is pinned down in §14 Q1.
  - Note: jars/manifest updates via Rigger polling `manifestServerUrl` (§9b) are a separate,
    always-on mechanism regardless of Static/Dynamic — that switch governs only
    `rigger.exe`/JRE binary updates (confirm in §14 Q2).
- **JRE source**: **no separate catalog/distributor API**. The manifest itself describes
  where a required JRE can be downloaded from, co-located with wherever the manifest JSON was
  fetched from (i.e. relative to the same base location as `manifestServerUrl`). Stage's
  provisioning routine downloads directly from that manifest-declared location.
- **Invocation mechanism**: on-demand provisioning is handled by a **small companion
  maintenance executable** (built from the same shared Stage codebase as the installer),
  left in the install root, which Rigger invokes when a needed JRE/rigger.exe update isn't
  present. Keeps `rigger.exe` itself lean; reuses the WebView2-based progress UI code.
- **Failure behavior**: confirmed — Rigger refuses to launch with a clear error if a required
  JRE can't be obtained, rather than running with a mismatched/older one.

## 14. Decisions (Round 6 — Static/Dynamic wiring)

- **Decision order**: install **scope is chosen first** (all-users vs per-user, as driven by
  elevation, per §2), and package **mode is derived/constrained by it**: all-users ⇒
  **forced Static** (Program Files isn't reliably writable by ordinary later launches);
  per-user ⇒ operator chooses Static or Dynamic, **defaulting to Dynamic**.
- **Switch scope**: Static/Dynamic governs **only `rigger.exe`/JRE binary updates**. Jars and
  manifest polling by Rigger (§9b) always happen regardless of package mode — even a Static
  install keeps receiving new app versions; it just never gets a new bundled runtime or
  launcher without an operator-driven reinstall/upgrade.
- **Admin + Dynamic**: **out of scope for v1**. All-users installs are always Static;
  Dynamic is only ever available for per-user installs. A writable, self-updating
  all-users-visible install is deferred.

## 15. Open Questions — resolved through Round 6 (superseded — see §16)

## 16. Installer Execution Flow (Round 7)

The operator specified the installer's backend execution sequence directly. Folded in and
reconciled against earlier decisions:

1. **Prerequisite check**
   - Disk space available at the target root (install dir) for the payload being extracted
     (`rigger.exe` + JRE + manifest — no jars, per §9b).
   - Target paths not locked by a running instance: check for a running `rigger.exe` and/or
     `java.exe` process rooted under the target install directory (relevant mainly on
     upgrade-in-place, §9b). If locked: **prompt the user to close the app**, with
     **Retry/Cancel** — the installer does not kill processes on its own.
2. **Extraction phase**
   - Write `rigger.exe`, the `jre/<version>/` directory, and `manifest.json` to the root app
     folder — `%ProgramFiles%\ABC` for all-users (confirmed), `%LocalAppData%\ABC` for
     per-user (§2).
3. **Registry & associations**
   - Create the standard Uninstall key and the `Software\ABC` key per §4 (including
     `InstallScope`, `PackageMode`, and the rest of that value set).
   - Register the custom protocol handler (§11) always.
   - Register file-type associations **only if the app's build-config declares any** — an
     **optional, empty-by-default** list of extensions; if none are declared, this step is
     simply skipped. No dedicated wizard UI for this in v1.
4. **Shortcut creation**
   - Start Menu: picked to **match `InstallScope`** — all-users install uses
     `%ProgramData%\Microsoft\Windows\Start Menu\Programs\<AppName>\`; per-user install uses
     `%AppData%\Microsoft\Windows\Start Menu\Programs\<AppName>\` instead (ProgramData isn't
     normally writable by a standard user).
   - Desktop: the shortcut goes on the current user's Desktop for a per-user install, or the
     Public/all-users Desktop for an all-users install.
   - Shortcut target is `rigger.exe`; description/name come from the manifest's
     `shortcut` block (§5).
5. **Post-install action (Finish page)**
   - A checkbox offering to **launch the application immediately**. Per operator
     clarification, the user-facing copy is phrased in terms of the **application** (e.g.
     "Launch ABC now"), never mentioning `rigger.exe`/"launcher" — Rigger is an
     implementation detail the end user never needs to see. Checking it runs `rigger.exe`
     (which then launches the JVM per the manifest) after Finish is clicked.

## 16b. Open Questions — none blocking for v1

Round 7 is resolved. All architecture-defining questions through Round 7 are closed.

## 17. Network Zones & Proxy Configuration (new requirement)

In addition to the DEV/TEST/PROD environment axis (§9b), Stage must support building/installing
for different **network zones** — the physical/logical network an install's users sit on, which
determines both which manifest server is reachable and what proxy (if any) is needed to reach
it. Three zones are named so far:

- **ABC Intranet** — the operator's own corporate network.
- **Internet** — a normal internet-connected client with no special network.
- **Radianz** — a private extranet (e.g. the BT Radianz financial-services network) some
  installs run on.

This is a distinct new build-time axis, not a rename of Environment — a build already varies by
Environment (DEV/TEST/PROD); it must now also vary by Network Zone, independently of it.

Requirements as specified by the operator:

1. **Per-zone manifest**: each network zone has its own manifest.json, since the server that
   provides it can differ by zone (an intranet-only manifest server is not reachable from the
   open internet, and vice versa). `manifestServerUrl` — already environment-specific (§9b) —
   must also become zone-specific.
2. **Proxy auto-detection**: Stage (installer) and Rigger (runtime) must auto-detect the
   system/network proxy needed to reach the manifest server and fetch resources, rather than
   requiring it to be hand-configured.
3. **Operator proxy override**: during installation, the operator can supply proxy information
   for the install's network zone explicitly, overriding what was auto-detected.
4. **Proxy + zone passed to the app**: since both are known at install time and at every app
   launch, Rigger passes the resolved network zone and proxy information into the launched Java
   app (as new runtime-injected placeholder values, extending §9b/§11's vocabulary), the same way
   `${installDir}`/`${environment}` are passed today.
5. **Zone round-trips through the auth flow**: the protocol-handler/token launch flow (§11) is
   extended so the web-based authentication request also carries the Network Zone, and the auth
   server's response (which becomes Rigger's invocation URI, per §11) carries Network Zone back.
   Rigger uses that returned zone value to look up which proxy to use.

## 18. Decisions (Round 8 — network zone & proxy) — CONFIRMED

§18b's four open questions were resolved directly with the operator, and the resulting design
was implemented (see the packages/files listed under each point):

- **Zone is a build-time input, like Environment** (§18b Q3: confirmed build-time, not an
  install-time wizard choice). `appconfig.EnvironmentConfig` gained a `Zones
  map[manifest.NetworkZone]ZoneConfig` (each zone declaring its own `manifestPath`/
  `manifestServerUrl`) — every (Environment, NetworkZone) pair needs its own manifest server, so
  there's no "zoneless" build. **Correction during implementation**: `NetworkZone` is
  deliberately **not** a closed enum like `Environment` — `DEV`/`TEST`/`PROD` are generic release
  stages that fit any app, but "ABC Intranet"/"Radianz" name this one company's specific network
  topology, and per §11b other apps reusing this tooling will have their own zone names. It's a
  validated free-form string (`internal/manifest.NetworkZone`, `ValidNetworkZone`), the same
  treatment `ProtocolScheme` already gets in `appconfig.go`.
- **One zone + one proxy per install, resolved once at install time** (§18b Q1: confirmed no
  multi-zone lookup table). The proxy is auto-detected, shown to the operator on a wizard step,
  optionally overridden, and stored as new `Software\<AppId>` registry values (`NetworkZone`
  required; `ProxyHost`/`ProxyPort` optional, empty meaning direct connection) —
  `internal/winreg.AppValues`. Rigger reads these at every launch; it never re-detects.
- **Proxy auto-detection uses WinHTTP's PAC-aware APIs** (§18b Q2: confirmed
  `WinHttpGetIEProxyConfigForCurrentUser`/`WinHttpGetProxyForUrl` over WinINET or a manual-only
  config). New package `internal/proxydetect` binds these directly (`golang.org/x/sys/windows`
  has no existing bindings), with pure-Go bypass-list/proxy-list parsing kept separately
  unit-testable, and a live test that exercises the real API on any Windows machine, matching the
  `internal/diskspace`/`internal/procscan` precedent. Detection is install-time-only.
  **Correction during implementation**: this closes a real functional gap, not just an
  installer-side concern — §17 item 2 requires *Rigger* to also use the resolved proxy "to reach
  the manifest server," but `cmd/rigger` previously built a bare `http.Client` that never
  consulted `AppValues.ProxyHost`/`ProxyPort` at all (only env vars). Fixed: `cmd/rigger`
  builds an `internal/proxydetect.Client` from the registry values and threads it through
  manifest fetching. Proxy authentication (username/password) is out of scope for v1 — not
  needed by either named zone today.
- **New runtime placeholders**: `${networkZone}`, `${proxyHost}`, `${proxyPort}` join
  `${installDir}`/`${dataDir}`/`${environment}` (§5) as built-in placeholders
  (`internal/manifest.PlaceholderContext`) Rigger substitutes into `jvmOptions`/`arguments`.
  `${proxyHost}`/`${proxyPort}` are dropped entirely (not empty-substituted) when no proxy is
  configured, extending the existing `${uri.X}`-drop rule.
- **Auth-flow round trip, resolved with no new URL-construction code** (§18b Q4: confirmed). The
  web auth page learns the zone via `${networkZone}` becoming e.g. `-Dabc.networkZone=...` in
  the manifest's `jvmOptions` — the *Java app* is what constructs the outbound auth request (and
  already gets `${environment}` today the same way); nothing in Stage/Rigger builds that URL.
  The auth server's redirect back to `abc://...` (§11) carries the zone back as a query param
  (convention: `networkZone`); `cmd/rigger` compares it against the installed `NetworkZone` and
  logs a warning on mismatch (§18b Q1's "consistency check, not a lookup" resolution) — non-fatal,
  no behavior change beyond the log line.

## 19. Doctor / Diagnostic Mode (new requirement — deferred)

The operator asked that "when Rigger runs it should inform what is being done." That's now
implemented for Rigger's existing fast launch path: tiered console + persistent-log-file
feedback (`internal/applog`, `<DataDir>/rigger.log`) records every step (invocation kind,
manifest load outcome, resolved Java runtime, final launch) and every warning/fatal error,
durably, regardless of whether a console is attached.

In the same discussion the operator described a much larger diagnostic capability for when a
launch doesn't just fail quietly but needs active troubleshooting: a **doctor mode** a user (or
a support agent walking them through it) can invoke to check what's wrong with an install and
help get a fix moving. This is **not built yet** — it needs UI infrastructure
(§11b's WebView2 embed) that doesn't exist anywhere in this repo — but is written up here so the
shape of the eventual work is on record, the same way §17 preceded §18's implementation.

Requirements as specified by the operator:

1. **Interactive diagnostic checks**, potentially slow (network calls, filesystem/registry
   validation), shown with real progress rather than a frozen console — reusing the
   WebView2-based UI shell (§11b) already planned for slow on-demand JRE provisioning (§12-14),
   rather than building a second UI technology just for this.
2. **Network reachability checks** scoped to what Rigger already legitimately knows about:
   whether the configured `ManifestServerUrl` is reachable, and the currently-resolved proxy
   status (`NetworkZone`/`ProxyHost`/`ProxyPort` from the registry, per §17-18).
3. **Registry/JRE integrity validation** — detecting and clearly reporting when an install's
   `Software\<AppId>` key is missing/corrupt (today this just fails `winreg.ReadAppValues` with a
   generic "reinstall" error, per `cmd/rigger/main.go`) or when the JRE directory the manifest
   expects doesn't exist or is incomplete.
4. **Log collection** — gathering the diagnostic evidence (at minimum `rigger.log`, per §6's
   logging work) into something a user can hand to support.
5. **"Contact support"** — a way to get collected diagnostics in front of a human, without
   Rigger needing any server-side mail-sending capability of its own.

## 20. Decisions (Round 9 — doctor mode) — CONFIRMED (write-up only; implementation deferred)

- **UI technology**: the same **WebView2 embed** from §11b is reused for doctor mode's
  interactive checks, exactly as it's reused for slow-operation progress (JRE provisioning,
  §12-14) — one UI shell, not two.
- **"Contact support" drafts a `mailto:` link**, not an in-app email send and not merely saving a
  diagnostic bundle to disk with no further action. The link is pre-filled with a subject/body
  containing a diagnostic summary and the **log file's path** (`mailto:` cannot carry
  attachments, so this is a path reference the user/support agent opens manually, not an attached
  bundle — deliberate, not an oversight).
- **Network checks are scoped to what Rigger already knows about** — manifest server
  reachability and proxy-detection status — and explicitly **exclude** any "auth endpoint"
  reachability check. The web auth page's URL (§11) is owned entirely by the Java app, not
  Rigger/Stage, so Rigger has no legitimate way to know that URL to test it (already resolved
  when §18 closed the equivalent open question for the auth round-trip).

## 20b. Open Questions — doctor mode (unresolved, blocking implementation)

- **Invocation trigger**: how is doctor mode entered? Candidates include a hidden CLI flag on
  `rigger.exe` (`rigger.exe --doctor`), a separate Start Menu shortcut/companion exe, or an
  automatic offer shown after N consecutive launch failures (and if so, tracked how — a registry
  counter? a scan of recent `rigger.log` entries?).
- **What a "network test" concretely checks and reports**: a bare TCP connect to the manifest
  server's host:port, a full HTTP(S) GET against `ManifestServerUrl` (and what counts as success
  — any 2xx? specifically expecting valid manifest JSON back?), what timeout applies, and whether
  the *proxy* check re-runs `internal/proxydetect` live or only redisplays the registry values
  resolved at install time (the latter is cheap/consistent with §18's "Rigger never re-detects"
  principle; the former would be a first exception to it).
- **How "registry needs reinstalling" is detected and presented**: is doctor mode's job simply to
  catch and render the *existing* `winreg.ReadAppValues` error more helpfully, or does it need
  deeper validation — e.g. confirming `InstallDir`/`DataDir` still exist on disk, or that the JRE
  directory contains a working `javaw.exe` rather than just existing as a directory?
- **Log collection scope and format**: which files get gathered beyond `rigger.log` (the Java
  app's own log, if any; Windows Event Log entries?), whether they're zipped, and where the
  resulting bundle is written (a fixed path under `DataDir`? a user-chosen "Save As" location
  offered by the WebView2 UI?).
- **Relationship to on-demand JRE provisioning's UI**: since that slow path is still an
  unimplemented `TODO(Phase 8)` stub (§12-14), it's undecided whether doctor mode's WebView2
  instance and provisioning's progress WebView2 instance are literally the same reusable
  component/window, or two separate uses of the same underlying technology.

## 21. Decisions (Round 10 — app-jar delivery & version retention) — CONFIRMED

Closes the retention-policy question §3 left open ("multiple version folders can coexist;
retention policy TBD") and implements the jar-delivery mechanism §9 already decided on
(Rigger fetches jars from `ManifestServerUrl`, never bundled by Stage).

- **Retention policy**: app-version folders directly under the install root follow the **same
  `MaxRetainedVersions=2`, keep-newest-by-modification-time policy already used for JRE versions**
  under `jre/` (§3) — evicted immediately after a successful fetch of a newer version, not on a
  delay or separate sweep. Chosen for consistency with the already-shipped JRE precedent rather
  than inventing a second policy; revisit only if a real deployment needs rollback further back
  than one version.
- **Fetch trigger**: on-demand, not eager — Rigger fetches a version's jars only when
  `layout.VersionDir(root, m.Version)` doesn't already exist on disk (first launch, or the
  manifest polled a newer `version` than what's currently unpacked). This mirrors the JRE-missing
  check already in `cmd/rigger` rather than re-fetching/re-extracting unchanged jars on every
  launch, which would also risk clobbering files a still-running JVM has open.
- **Download convention**: `Manifest.ArtifactDownloadURL()` derives the archive location from
  `ManifestServerUrl` by the same convention as `Manifest.DownloadURL()` for JRE archives — the
  manifest's own final path segment replaced with `artifacts/<version>.zip`.
- **Checksum**: a new manifest field, `artifactSha256`, verifies the downloaded archive — the
  app-level analogue of `runtime.sha256`, with the same looseness (documented as required for
  on-demand delivery, not enforced by `Manifest.Validate()`, matching how `runtime.sha256` is
  already handled).
- **Package-mode independence**: this fetch runs unconditionally, regardless of `PackageMode` —
  it is the always-on jars/manifest channel from §9b/§13-14, not the Static/Dynamic-gated
  rigger.exe/JRE self-update channel.
- **Proxy routing**: routed through the same `proxydetect`-aware `*http.Client` `cmd/rigger`
  already builds for the manifest refresh (§17-18), not a bare client — a live per-launch network
  call from an ordinary user session has the same proxy-correctness requirement as the manifest
  fetch does.

## 22. Decisions (Round 11 — dropping the `cmd/maintain` companion exe) — CONFIRMED, SUPERSEDES §13

§13's "Invocation mechanism" decision ("a small companion maintenance executable... which Rigger
invokes when a needed JRE/rigger.exe update isn't present") was implemented once
(`cmd/maintain`/`maintain.exe`) and then reverted after review: shipping and installing a third
executable alongside `rigger.exe`/`unins.exe` was judged not advisable — every additional exe on
the client machine needs its own Authenticode signature and has to separately build up
AV/SmartScreen reputation, an ongoing operational cost with no matching benefit here. An
uninstaller exe remains accepted as necessary (a Windows install directory genuinely can't delete
its own running exe), but a *second* generic companion binary is not.

- **On-demand JRE provisioning now runs in-process inside `rigger.exe`** (`internal/jreprovision.
  Provision`, called directly from `cmd/rigger`'s `run()`), not via a subprocess. The constraint
  that originally motivated a separate process — a running program generally can't overwrite its
  own executable file — doesn't apply here: provisioning a JRE writes into `jre/<version>`, never
  into `rigger.exe`'s own file, so there is no structural reason it needs to run out-of-process.
- **`rigger.exe` binary self-update** (the other half of §13's framing, still unimplemented per
  §21/§7) will **not** get a dedicated companion exe either when it's eventually built. It will
  reuse the self-copy-to-`%TEMP%`-and-relaunch pattern `unins.exe` already uses on itself:
  `rigger.exe` copies itself to `%TEMP%`, runs that copy with a flag, the copy overwrites the real
  `rigger.exe` in the install root, then relaunches it. No new binary is shipped or signed; the
  `%TEMP%` copy is transient, not an installed artifact.
- **`cmd/maintain` is deleted**, not left as an empty placeholder — unlike `internal/signing`/
  `internal/riggerupdate`, which remain reserved for genuinely unresolved future work, this
  direction was tried, reconsidered, and is not the intended design going forward.

## 23. Decisions (Round 12 — `cmd/installer` UI: terminal default + browser wizard) — CONFIRMED, SUPERSEDES §11B

§11b's "embedded Microsoft Edge WebView2 control" decision was implemented far enough to
discover its real costs, then abandoned in favor of a different design. Both are recorded here
because the reasoning — not just the outcome — is what future changes need to respect.

**Why WebView2 was abandoned:**
- It requires Go to *implement* COM callback interfaces (WebView2's environment/controller
  creation is asynchronous, calling back into completion handlers the caller must supply) — a
  new kind of Windows interop this codebase had never needed; every existing package
  (`internal/shortcut` included) only ever calls *into* COM, never implements it.
- It requires a hand-rolled Win32 window and message loop — also new territory, and
  `golang.org/x/sys/windows` provides neither (confirmed by inspection: no `RegisterClassEx`,
  `CreateWindowEx`, `GetMessage`, or `DispatchMessage`, only window-query functions).
- It depends on `WebView2Loader.dll`, a native binary distinct from the WebView2 *Runtime*
  (which genuinely is preinstalled on the Windows 10 1803+/11 target) — the Loader is not, per
  Microsoft's own distribution guidance, and three separate constraints blocked sourcing it
  cleanly: org policy disallows committing binaries to source control, the GitLab CI build image
  is Linux (not a problem for this pure-Go repo generally, but ruling out any approach requiring
  a Windows-side fetch/link step), and the pipeline's only network egress is through a local
  Nexus repository whose ability to proxy the relevant feed was unconfirmed.
- It would have been effectively untestable — no unit tests possible, only real-Windows manual
  verification, unlike every other package in this repo.

**Final design — `cmd/installer` has two real UIs, chosen by a `-gui` flag:**
- **Default: `internal/tui`**, a polished terminal wizard built on
  `github.com/charmbracelet/bubbletea` + `lipgloss`. This is **the first third-party Go
  dependency this repo has added beyond `golang.org/x/sys`** — a deliberate exception, made
  because the terminal path is no longer a temporary stand-in the way `internal/console`
  originally was; it's the permanent default, so it needs to look genuinely good. Both libraries
  are pure Go (no CGO), preserving Linux-runner cross-compilation.
- **`-gui`: `internal/wizard`**, a local `net/http` server (Go `html/template` + HTMX for
  interactivity) opened as a chrome-less Edge "app window" (`msedge.exe --app=...`, falling back
  to the OS default browser if Edge isn't found at its known path). HTMX is plain JS **source**
  checked into the repo like any other file — none of `WebView2Loader.dll`'s binary/supply-chain
  concerns apply to it.
- `internal/console` is untouched and keeps serving `cmd/uninstaller`, whose simple confirm/retry
  flow doesn't need this treatment.
- Neither UI conflicts with §9's "Silent install: v1 targets the GUI wizard only... CLI flags
  deferred" — `-gui` is a presentation switch, not a silent/unattended mode; both paths require
  the same interactive confirmations.

**Effect on other sections referencing the WebView2 shell**: §12's on-demand-JRE-provisioning
progress UI and §19-20's doctor-mode UI both assumed reuse of "the WebView2-based UI shell."
That shell no longer exists in that form. Either future effort should target `internal/tui`/
`internal/wizard` instead, re-evaluating reuse feasibility at that time — this is noted here
rather than by editing §12/§19-20's original text, consistent with how §22 superseded §13.

## 24. Decisions (Round 13 — doctor mode implementation) — CONFIRMED, RESOLVES §20B

§20b left four questions blocking implementation. Resolved directly with the operator:

- **Invocation trigger**: `rigger.exe --doctor`, checked directly against `argv[1]` (not the
  `flag` package, so it doesn't interfere with the existing raw-`argv[1]` protocol-handler URI
  detection). No separate shortcut, no automatic-offer-after-N-failures tracking — simplest
  option, no new state to maintain.
- **UI technology**: §20's "reuse the WebView2-based UI shell" is moot (§23). Doctor mode reuses
  **`internal/wizard`**, not `internal/tui` — `internal/wizard` is stdlib-only (`net/http` +
  `html/template`), so it adds zero new dependencies to `rigger.exe`'s every-launch binary, unlike
  `internal/tui` which would pull in `bubbletea`/`lipgloss`. A browser page also gives the
  already-decided `mailto:` "Contact Support" link (§20) native support.
- **Network test semantics**: a full HTTP GET against `ManifestServerUrl`, parsed as a manifest
  (success = HTTP 200 + valid manifest JSON, not just "the port answered") — not a bare TCP
  connect. Proxy status is the registry's already-resolved `ProxyHost`/`ProxyPort`, not a live
  `internal/proxydetect` re-run — no exception to §18's "Rigger never re-detects" principle.
- **Registry/JRE validation depth**: deep, not shallow. Beyond "does `Software\<AppId>` exist":
  confirm `InstallDir`/`DataDir` still exist on disk, and that the JRE directory the manifest
  names actually contains `javaw.exe` (not just that the directory exists). A failed registry
  read is itself the worst case doctor mode exists to help with — it's caught and rendered as one
  clear failed check (naming the fix: reinstall) rather than doctor mode itself crashing; no
  further checks can run without `InstallDir`/`DataDir`, so the report is deliberately short in
  that case, not broken.

**Log collection** (§19 point 4, previously unresolved on scope/format): every `*.log` file found
directly under `DataDir` — covers `rigger.log` plus whatever the launched app itself writes there
(e.g. `abc-launch.log` in the fixture), without hardcoding any app-specific filename, since Stage
supports arbitrary apps — zipped to `<DataDir>/diagnostics-<timestamp>.zip`. No "Save As" dialog;
the path is simply shown on the page (and included in the `mailto:` body per §20's decision that
the link carries a path reference, not an attachment).

**New manifest field**: `supportEmail` (optional) — the `mailto:` recipient for "Contact Support"
(§20 decided the mechanism; it didn't say where the address comes from). Lives in the manifest,
not the registry, so it can change without a reinstall, consistent with everything else
`ManifestServerUrl` polling already refreshes. Empty disables the button entirely.

Implementation: `internal/doctor` (pure diagnostic logic — `Run`, `CollectLogs` — fully
unit-tested except the registry read itself, which needs real Windows state like other
Windows-native packages in this repo) + `internal/wizard`'s new `ShowDoctorReport` (a single
static results page, no multi-screen flow — the checks are fast enough that no progress UI is
needed, unlike §19's original "potentially slow" framing, which was written before knowing what
the actual checks would be). Manually verified end-to-end on a real install, including the worst
case (a corrupted registry key) rendering one clear, actionable failure rather than crashing.

## 25. Decisions (Round 14 — `rigger.exe` binary self-update) — CONFIRMED

§13/§14 already decided the mechanism (self-copy-to-`%TEMP%`-and-relaunch, no companion exe,
Dynamic-mode-gated) and §22 confirmed no companion exe would be needed. What was still missing —
a versioning scheme for `rigger.exe` itself, and where the update declaration/artifact live —
is resolved here.

- **Versioning scheme**: a hardcoded `const Version` in `internal/riggerupdate`, bumped manually
  per Stage release. This repo doesn't use git tags today, and introducing that infra (tag-driven
  `-ldflags` injection) wasn't warranted just for this — the simplest option that works.
- **New manifest field**: `rigger: {version, sha256}` (`manifest.RiggerSpec`), mirroring
  `RuntimeSpec`'s shape. Lives in the manifest, not the registry, consistent with `supportEmail`
  (§24) and the JRE/jar fields — it's exactly the kind of thing `ManifestServerUrl` polling
  already refreshes every launch. A declared `version` with no `sha256` is treated as
  misconfigured and skipped, never trusted (mirrors `RuntimeSpec.SHA256`'s existing looseness).
- **Download convention**: `Manifest.RiggerDownloadURL()`, the same pattern as `DownloadURL()`/
  `ArtifactDownloadURL()` — the manifest's final path segment replaced with
  `rigger/<version>-win-x64.exe`.
- **Distribution artifact**: `stagebuild` now copies the freshly-built `rigger.exe` to
  `dist/rigger-<version>-win-x64.exe` and prints the exact manifest snippet (version + computed
  checksum) an operator needs to declare — closing a real gap this feature surfaced: `rigger.exe`
  previously only ever existed embedded inside a specific app's installer, with no standalone
  artifact an operator could ever upload to their manifest server for self-update to fetch. Stage
  still doesn't upload anywhere itself, matching how JRE/jar archives already work — the
  operator's own deployment pipeline owns the manifest server.
- **Failure behavior**: any failure — spawning the `%TEMP%` copy, downloading, the checksum
  check — falls back to relaunching the existing, unmodified `rigger.exe` rather than failing the
  launch outright. An update attempt must never be why the app doesn't open. Manually verified:
  a genuine end-to-end update (old binary detects a new version, downloads, verifies, overwrites,
  relaunches — confirmed via checksum that the installed binary was actually replaced, and that
  the relaunched, now-current binary does *not* re-trigger another update, avoiding a loop), and
  a real failure path (a `%TEMP%` file-write collision from two rapid update attempts) correctly
  falling back to launching the prior version rather than failing.
- **Known limitation, accepted for v1**: `BeginSelfUpdate` writes to a fixed temp filename
  (`stage-rigger-update-<appID>.exe`), so two self-update attempts in quick succession (before
  Windows has released the first attempt's file handle) can collide — observed directly during
  manual testing. The collision is caught by the general failure-falls-back-to-current-version
  behavior above (the second attempt just fails cleanly and the app still launches on the
  existing version), and self-heals on the next launch once the earlier temp process has fully
  exited. Given real version bumps happen far apart in practice, a randomized temp filename to
  close this narrow race wasn't judged worth the complexity for v1.
