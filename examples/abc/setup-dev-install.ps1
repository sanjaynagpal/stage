# Assembles a real per-user ABC install layout under %LocalAppData%\ABC so
# rigger.exe can be manually tested end to end, standing in for the
# installer/stagebuild that don't exist yet (docs/REQUIREMENTS.md §16).
#
# Concretely this: builds rigger.exe, builds the ABC fixture jar, copies the
# local JDK (from JAVA_HOME) into jre/<version>, substitutes that version
# into the fixture manifest, writes it both as the install's local cache and
# as the copy fakeserver serves, and writes the HKCU\Software\ABC registry
# values Rigger reads (internal/winreg.AppValues) — everything a real
# installer would do, minus registering shortcuts/protocol handler/uninstall
# key, which aren't needed to launch rigger.exe directly for a test.
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

$versionDir = Join-Path $installRoot "1.0.0"
New-Item -ItemType Directory -Force -Path $versionDir | Out-Null
Copy-Item -Force (Join-Path $abcDir "java/target/app.jar") (Join-Path $versionDir "app.jar")

$jreDir = Join-Path $installRoot "jre/$javaVersion"
if (-not (Test-Path $jreDir)) {
    Write-Host "`nCopying local JDK into $jreDir (one-time, may take a minute)..."
    New-Item -ItemType Directory -Force -Path $jreDir | Out-Null
    Copy-Item -Recurse -Force (Join-Path $env:JAVA_HOME "*") $jreDir
} else {
    Write-Host "`n$jreDir already exists, skipping JDK copy."
}

Write-Host "`nWriting manifest.json (local cache + fakeserver-served copy)..."
$template = Get-Content (Join-Path $abcDir "fakeserver/public/abc/manifest.json") -Raw
$substituted = $template.Replace("__JAVA_VERSION__", $javaVersion)
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
Write-Host "  2. Launch via shortcut path:"
Write-Host "       & '$installRoot\rigger.exe'"
Write-Host "  3. Launch via protocol-handler path (token gets injected as -Dabc.token):"
Write-Host "       & '$installRoot\rigger.exe' '${protocolScheme}://launch?token=demo123'"
Write-Host "  4. Same, but with a mismatched Network Zone to see the consistency-check warning:"
Write-Host "       & '$installRoot\rigger.exe' '${protocolScheme}://launch?token=demo123&networkZone=Radianz'"
Write-Host "  5. Log written to: $dataDir\abc-launch.log"
Write-Host "  6. Rigger's own log: $dataDir\rigger.log"
