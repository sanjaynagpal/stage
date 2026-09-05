# Prepares real fixtures for testing cmd/stagebuild end to end: zips the
# local JDK (from JAVA_HOME) into a JRE archive, computes its checksum, and
# writes resolved (placeholder-substituted) copies of the manifest and
# appconfig templates stagebuild consumes. All generated files are
# gitignored (large/machine-specific); rerun this whenever JAVA_HOME
# changes.
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

Write-Host "`nWriting manifest.generated.json..."
$manifestTemplate = Get-Content (Join-Path $abcDir "fakeserver/public/abc/manifest.json") -Raw
$manifestResolved = $manifestTemplate.Replace("__JAVA_VERSION__", $javaVersion)
Set-Content -Path (Join-Path $abcDir "manifest.generated.json") -Value $manifestResolved -NoNewline

Write-Host "Writing appconfig.generated.json..."
$appconfigTemplate = Get-Content (Join-Path $abcDir "appconfig.json") -Raw
$appconfigResolved = $appconfigTemplate.Replace("__JRE_VERSION__", $javaVersion).Replace("__JRE_SHA256__", $sha256)
Set-Content -Path (Join-Path $abcDir "appconfig.generated.json") -Value $appconfigResolved -NoNewline

Write-Host "`nDone. Next step:"
Write-Host "  go run ./cmd/stagebuild -config examples/abc/appconfig.generated.json -env PROD -zone Internet"
