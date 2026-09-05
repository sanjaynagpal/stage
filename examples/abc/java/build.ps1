# Compiles the ABC fixture app (Main.java) into target/app.jar.
# Requires javac/jar on PATH (or JAVA_HOME set). Output goes under target/,
# which is gitignored, matching how a real app's jars are never committed.
$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$classesDir = Join-Path $here "target/classes"
$jarPath = Join-Path $here "target/app.jar"

New-Item -ItemType Directory -Force -Path $classesDir | Out-Null

$javac = if ($env:JAVA_HOME) { Join-Path $env:JAVA_HOME "bin/javac.exe" } else { "javac" }
$jar = if ($env:JAVA_HOME) { Join-Path $env:JAVA_HOME "bin/jar.exe" } else { "jar" }

& $javac -d $classesDir (Join-Path $here "src/com/example/abc/Main.java")
if ($LASTEXITCODE -ne 0) { throw "javac failed" }

& $jar --create --file $jarPath --main-class com.example.abc.Main -C $classesDir .
if ($LASTEXITCODE -ne 0) { throw "jar failed" }

Write-Host "Built $jarPath"
