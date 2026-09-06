# Prepares real fixtures for testing cmd/stagebuild end to end: zips the
# local JDK (from JAVA_HOME) into a JRE archive, computes its checksum, also
# copies that archive to fakeserver's servable jre/ location for testing
# Rigger's own in-process on-demand JRE provisioning (docs/REQUIREMENTS.md
# §12-14, §22), builds the ABC fixture jar and zips it into the artifacts archive
# fakeserver serves for Rigger's on-demand jar fetch (internal/jarprovision,
# docs/REQUIREMENTS.md §9 — the gap that used to make a real install's
# "Launch now" fail), and writes resolved (placeholder-substituted) copies
# of the manifest and appconfig templates stagebuild consumes. All generated
# files are gitignored (large/machine-specific); rerun this whenever
# JAVA_HOME changes.
$ErrorActionPreference = "Stop"

$abcDir = Split-Path -Parent $MyInvocation.MyCommand.Path

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

$zipPath = Join-Path $abcDir "jre-archive.zip"
Write-Host "`nZipping $env:JAVA_HOME into $zipPath (may take a minute)..."
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }
Compress-Archive -Path (Join-Path $env:JAVA_HOME "*") -DestinationPath $zipPath -CompressionLevel Optimal

$sha256 = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLower()
Write-Host "SHA256: $sha256"

Write-Host "`nCopying the JRE archive to fakeserver's servable jre/ location (matches"
Write-Host "Manifest.DownloadURL()'s convention, for testing Rigger's own in-process"
Write-Host "on-demand provisioning against a real stagebuild-built install)..."
$jreServedDir = Join-Path $abcDir "fakeserver/served/abc/jre"
New-Item -ItemType Directory -Force -Path $jreServedDir | Out-Null
Copy-Item -Force $zipPath (Join-Path $jreServedDir "$javaVersion-win-x64.zip")

Write-Host "`nBuilding the ABC fixture app..."
& (Join-Path $abcDir "java/build.ps1")

Write-Host "`nZipping app.jar into the artifacts archive fakeserver will serve..."
$artifactsDir = Join-Path $abcDir "fakeserver/served/abc/artifacts"
New-Item -ItemType Directory -Force -Path $artifactsDir | Out-Null
$artifactZipPath = Join-Path $artifactsDir "1.0.0.zip"
if (Test-Path $artifactZipPath) { Remove-Item $artifactZipPath -Force }
Compress-Archive -Path (Join-Path $abcDir "java/target/app.jar") -DestinationPath $artifactZipPath -CompressionLevel Optimal
$artifactSha256 = (Get-FileHash -Path $artifactZipPath -Algorithm SHA256).Hash.ToLower()
Write-Host "Artifact SHA256: $artifactSha256"

Write-Host "`nWriting manifest.generated.json..."
$manifestTemplate = Get-Content (Join-Path $abcDir "fakeserver/public/abc/manifest.json") -Raw
$manifestResolved = $manifestTemplate.Replace("__JAVA_VERSION__", $javaVersion).Replace("__ARTIFACT_SHA256__", $artifactSha256).Replace("__JRE_SHA256__", $sha256)
Set-Content -Path (Join-Path $abcDir "manifest.generated.json") -Value $manifestResolved -NoNewline

Write-Host "`nWriting fakeserver-served copy of the manifest..."
$servedDir = Join-Path $abcDir "fakeserver/served/abc"
New-Item -ItemType Directory -Force -Path $servedDir | Out-Null
Set-Content -Path (Join-Path $servedDir "manifest.json") -Value $manifestResolved -NoNewline

Write-Host "Writing appconfig.generated.json..."
$appconfigTemplate = Get-Content (Join-Path $abcDir "appconfig.json") -Raw
$appconfigResolved = $appconfigTemplate.Replace("__JRE_VERSION__", $javaVersion).Replace("__JRE_SHA256__", $sha256)
Set-Content -Path (Join-Path $abcDir "appconfig.generated.json") -Value $appconfigResolved -NoNewline

Write-Host "`nDone. Next steps:"
Write-Host "  1. go run ./cmd/stagebuild -config examples/abc/appconfig.generated.json -env PROD -zone Internet"
Write-Host "  2. Before testing 'Launch now', start fakeserver so Rigger can fetch the app jars:"
Write-Host "       go run ./examples/abc/fakeserver -dir examples/abc/fakeserver/served"
