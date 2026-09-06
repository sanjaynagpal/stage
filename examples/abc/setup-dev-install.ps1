# Assembles a real per-user ABC install layout under %LocalAppData%\ABC so
# rigger.exe can be manually tested end to end, standing in for the
# installer/stagebuild that don't exist yet (docs/REQUIREMENTS.md §16).
#
# Concretely this: builds rigger.exe, builds the ABC fixture jar and zips it
# into an artifacts archive served by fakeserver (deliberately NOT copied
# into the install's version directory — the whole point is to exercise
# Rigger's on-demand jar fetch, internal/jarprovision, on first launch,
# docs/REQUIREMENTS.md §9), copies the local JDK (from JAVA_HOME) into
# jre/<version>, substitutes that version/checksum into the fixture
# manifest, writes it both as the install's local cache and as the copy
# fakeserver serves, and writes the HKCU\Software\ABC registry values Rigger
# reads (internal/winreg.AppValues) — everything a real installer would do,
# minus registering shortcuts/protocol handler/uninstall key, which aren't
# needed to launch rigger.exe directly for a test.
#
# Run once, then see the printed instructions to start fakeserver and
# launch rigger.exe.
$ErrorActionPreference = "Stop"

$abcDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent (Split-Path -Parent $abcDir)
$installRoot = Join-Path $env:LocalAppData "ABC"
$dataDir = Join-Path $env:AppData "ABC"
$manifestServerUrl = "http://127.0.0.1:8080/abc/manifest.json"
$protocolScheme = "acme-abc"
$networkZone = "Internet"

if (-not $env:JAVA_HOME) {
    throw "JAVA_HOME is not set; point it at a local JDK/JRE to use as the fixture's runtime."
}
$releaseFile = Join-Path $env:JAVA_HOME "release"
if (-not (Test-Path $releaseFile)) {
    throw "No 'release' file found under JAVA_HOME ($env:JAVA_HOME); expected a JDK/JRE install."
}
$versionLine = Select-String -Path $releaseFile -Pattern '^(FULL_VERSION|JAVA_VERSION)="(.+)"$' | Select-Object -First 1
if (-not $versionLine) {
    throw "Could not parse a version out of $releaseFile"
}
$javaVersion = $versionLine.Matches[0].Groups[2].Value
Write-Host "Using local JDK version $javaVersion from $env:JAVA_HOME"

Write-Host "`nBuilding rigger.exe..."
New-Item -ItemType Directory -Force -Path $installRoot | Out-Null
Push-Location $repoRoot
try {
    go build -o (Join-Path $installRoot "rigger.exe") ./cmd/rigger
    if ($LASTEXITCODE -ne 0) { throw "go build ./cmd/rigger failed" }
} finally {
    Pop-Location
}

Write-Host "`nBuilding the ABC fixture app..."
& (Join-Path $abcDir "java/build.ps1")

Write-Host "`nZipping app.jar into an artifacts archive for fakeserver to serve..."
$artifactsDir = Join-Path $abcDir "fakeserver/served/abc/artifacts"
New-Item -ItemType Directory -Force -Path $artifactsDir | Out-Null
$artifactZipPath = Join-Path $artifactsDir "1.0.0.zip"
if (Test-Path $artifactZipPath) { Remove-Item $artifactZipPath -Force }
Compress-Archive -Path (Join-Path $abcDir "java/target/app.jar") -DestinationPath $artifactZipPath -CompressionLevel Optimal
$artifactSha256 = (Get-FileHash -Path $artifactZipPath -Algorithm SHA256).Hash.ToLower()
Write-Host "Artifact SHA256: $artifactSha256"

# Deliberately not pre-populating $installRoot\1.0.0 with app.jar here: the
# absence is what makes the first launch exercise Rigger's on-demand jar
# fetch against the archive just written above.

$jreDir = Join-Path $installRoot "jre/$javaVersion"
if (-not (Test-Path $jreDir)) {
    Write-Host "`nCopying local JDK into $jreDir (one-time, may take a minute)..."
    New-Item -ItemType Directory -Force -Path $jreDir | Out-Null
    Copy-Item -Recurse -Force (Join-Path $env:JAVA_HOME "*") $jreDir
} else {
    Write-Host "`n$jreDir already exists, skipping JDK copy."
}

# Also zip the JDK into fakeserver's servable jre/ location, matching
# Manifest.DownloadURL()'s convention, so Rigger's own in-process on-demand
# JRE provisioning (internal/jreprovision.Provision, docs/REQUIREMENTS.md
# §12-14, §22) can be manually tested by deleting $jreDir and relaunching —
# see docs/TESTING.md.
$jreArchivePath = Join-Path $abcDir "fakeserver/served/abc/jre/$javaVersion-win-x64.zip"
if (-not (Test-Path $jreArchivePath)) {
    Write-Host "`nZipping local JDK into $jreArchivePath for on-demand provisioning testing (one-time, may take a minute)..."
    New-Item -ItemType Directory -Force -Path (Split-Path $jreArchivePath) | Out-Null
    Compress-Archive -Path (Join-Path $env:JAVA_HOME "*") -DestinationPath $jreArchivePath -CompressionLevel Optimal
} else {
    Write-Host "`n$jreArchivePath already exists, skipping JRE zip."
}
$jreSha256 = (Get-FileHash -Path $jreArchivePath -Algorithm SHA256).Hash.ToLower()
Write-Host "JRE SHA256: $jreSha256"

Write-Host "`nWriting manifest.json (local cache + fakeserver-served copy)..."
$template = Get-Content (Join-Path $abcDir "fakeserver/public/abc/manifest.json") -Raw
$substituted = $template.Replace("__JAVA_VERSION__", $javaVersion).Replace("__ARTIFACT_SHA256__", $artifactSha256).Replace("__JRE_SHA256__", $jreSha256)
Set-Content -Path (Join-Path $installRoot "manifest.json") -Value $substituted -NoNewline

$servedDir = Join-Path $abcDir "fakeserver/served/abc"
New-Item -ItemType Directory -Force -Path $servedDir | Out-Null
Set-Content -Path (Join-Path $servedDir "manifest.json") -Value $substituted -NoNewline

Write-Host "`nWriting HKCU:\Software\ABC..."
$key = "HKCU:\Software\ABC"
New-Item -Path $key -Force | Out-Null
Set-ItemProperty -Path $key -Name "InstallScope" -Value "PerUser"
Set-ItemProperty -Path $key -Name "PackageMode" -Value "Dynamic"
Set-ItemProperty -Path $key -Name "AppId" -Value "ABC"
Set-ItemProperty -Path $key -Name "InstallDir" -Value $installRoot
Set-ItemProperty -Path $key -Name "DataDir" -Value $dataDir
Set-ItemProperty -Path $key -Name "ManifestServerUrl" -Value $manifestServerUrl
Set-ItemProperty -Path $key -Name "ProtocolScheme" -Value $protocolScheme
Set-ItemProperty -Path $key -Name "DisplayVersion" -Value "1.0.0"
Set-ItemProperty -Path $key -Name "NetworkZone" -Value $networkZone
# ProxyHost/ProxyPort intentionally left unset: this machine has no
# configured proxy (confirmed via internal/proxydetect's live test and
# `netsh winhttp show proxy`), so an empty value correctly models a
# direct-connection install. Set them here to test the proxy-placeholder
# path against a real (or fake) proxy.

Write-Host "`nDone. Install root: $installRoot`n"
Write-Host "Next steps:"
Write-Host "  1. Start fakeserver (separate shell):"
Write-Host "       go run ./examples/abc/fakeserver -dir examples/abc/fakeserver/served"
Write-Host "  2. Launch via shortcut path (first launch fetches and unpacks the app-jars"
Write-Host "     archive on demand, since $installRoot\1.0.0 doesn't exist yet):"
Write-Host "       & '$installRoot\rigger.exe'"
Write-Host "  3. Launch via protocol-handler path (token gets injected as -Dabc.token):"
Write-Host "       & '$installRoot\rigger.exe' '${protocolScheme}://launch?token=demo123'"
Write-Host "  4. Same, but with a mismatched Network Zone to see the consistency-check warning:"
Write-Host "       & '$installRoot\rigger.exe' '${protocolScheme}://launch?token=demo123&networkZone=Radianz'"
Write-Host "  5. Log written to: $dataDir\abc-launch.log"
Write-Host "  6. Rigger's own log: $dataDir\rigger.log"
Write-Host "  7. To test on-demand JRE provisioning (in-process, PackageMode=Dynamic here),"
Write-Host "     delete $installRoot\jre\$javaVersion and relaunch — see docs/TESTING.md §2.5"
