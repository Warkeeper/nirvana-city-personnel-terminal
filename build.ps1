[CmdletBinding()]
param(
    [ValidateSet("windows", "darwin")]
    [string]$TargetOS = "windows",

    [ValidateSet("amd64", "arm64")]
    [string]$TargetArch = "amd64"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$supportedTargets = @(
    "windows/amd64",
    "darwin/amd64",
    "darwin/arm64"
)
$target = "$TargetOS/$TargetArch"
if ($target -notin $supportedTargets) {
    throw "Unsupported target '$target'. Supported targets: $($supportedTargets -join ', ')."
}

$repoRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$requiredFrontendFiles = @(
    "frontend/index.html",
    "frontend/static/app.js",
    "frontend/static/vendor/vue/dist/vue.js",
    "frontend/static/vendor/element-ui/lib/index.js",
    "frontend/static/vendor/element-ui/lib/theme-chalk/index.css",
    "frontend/static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.woff",
    "frontend/static/vendor/element-ui/lib/theme-chalk/fonts/element-icons.ttf"
)

$originalEnvironment = @{
    CGO_ENABLED = [Environment]::GetEnvironmentVariable("CGO_ENABLED", "Process")
    GOOS        = [Environment]::GetEnvironmentVariable("GOOS", "Process")
    GOARCH      = [Environment]::GetEnvironmentVariable("GOARCH", "Process")
    GOCACHE     = [Environment]::GetEnvironmentVariable("GOCACHE", "Process")
}

Push-Location $repoRoot
try {
    foreach ($relativePath in $requiredFrontendFiles) {
        if (-not (Test-Path -LiteralPath $relativePath -PathType Leaf)) {
            throw "Required frontend file is missing: $relativePath"
        }
    }

    if (-not (Get-Command node -ErrorAction SilentlyContinue)) {
        throw "Node.js is required to check frontend/static/app.js before building."
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "Go is required to build ./cmd/ncpt."
    }

    Write-Host "Checking frontend/static/app.js ..."
    & node --check "frontend/static/app.js"
    if ($LASTEXITCODE -ne 0) {
        throw "Frontend JavaScript syntax check failed."
    }

    $goCache = Join-Path $repoRoot ".tmp-go-build-cache"
    $outputDir = Join-Path $repoRoot "dist"
    New-Item -ItemType Directory -Force -Path $goCache | Out-Null
    New-Item -ItemType Directory -Force -Path $outputDir | Out-Null

    $extension = if ($TargetOS -eq "windows") { ".exe" } else { "" }
    $outputName = "ncpt-$TargetOS-$TargetArch$extension"
    $outputPath = Join-Path $outputDir $outputName

    $env:CGO_ENABLED = "0"
    $env:GOOS = $TargetOS
    $env:GOARCH = $TargetArch
    $env:GOCACHE = $goCache

    Write-Host "Building $target ..."
    & go build -trimpath -o $outputPath ./cmd/ncpt
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed for $target."
    }

    $artifact = Get-Item -LiteralPath $outputPath
    $hash = Get-FileHash -Algorithm SHA256 -LiteralPath $outputPath

    Write-Host ""
    Write-Host "Build succeeded."
    Write-Host "Output : $($artifact.FullName)"
    Write-Host "Size   : $($artifact.Length) bytes"
    Write-Host "SHA256 : $($hash.Hash)"
}
finally {
    foreach ($name in $originalEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable(
            $name,
            $originalEnvironment[$name],
            "Process"
        )
    }
    Pop-Location
}
