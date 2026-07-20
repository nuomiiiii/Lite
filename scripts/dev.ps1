[CmdletBinding()]
param(
    [ValidateSet("server", "web", "build", "test")]
    [string]$Mode = "server",
    [string]$Listen = "127.0.0.1:25774"
)

$ErrorActionPreference = "Stop"

$repo = Split-Path $PSScriptRoot -Parent
$workspace = Split-Path $repo -Parent
$toolRoot = Join-Path $workspace "work\tools"
$webRepo = Join-Path $workspace "komari-web"

function Resolve-Executable {
    param(
        [string]$PortablePath,
        [string]$CommandName
    )

    if (Test-Path -LiteralPath $PortablePath) {
        return $PortablePath
    }

    $command = Get-Command $CommandName -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Required tool '$CommandName' was not found."
    }
    return $command.Source
}

$go = Resolve-Executable (Join-Path $toolRoot "go\go\bin\go.exe") "go.exe"
$npm = Resolve-Executable (Join-Path $toolRoot "node\node-v24.18.0-win-x64\npm.cmd") "npm.cmd"
$zig = Resolve-Executable (Join-Path $toolRoot "zig\zig-x86_64-windows-0.14.1\zig.exe") "zig.exe"

$env:PATH = "$(Split-Path $go);$(Split-Path $npm);$(Split-Path $zig);$env:PATH"
$env:GOPATH = Join-Path $workspace "work\gopath"
$env:GOCACHE = Join-Path $workspace "work\gocache"
$env:ZIG_GLOBAL_CACHE_DIR = Join-Path $workspace "work\zig-global-cache"
$env:ZIG_LOCAL_CACHE_DIR = Join-Path $workspace "work\zig-local-cache"
$env:CGO_ENABLED = "1"
$env:CC = "$zig cc"

function Sync-Frontend {
    if (-not (Test-Path -LiteralPath (Join-Path $webRepo "package.json"))) {
        throw "Komari Web repository was not found at $webRepo"
    }

    Push-Location $webRepo
    try {
        if (-not (Test-Path -LiteralPath (Join-Path $webRepo "node_modules"))) {
            & $npm install
        }
        & $npm run build
        if ($LASTEXITCODE -ne 0) {
            throw "Frontend build failed with exit code $LASTEXITCODE."
        }
    } finally {
        Pop-Location
    }

    $target = Join-Path $repo "web\public\defaultTheme"
    $targetDist = Join-Path $target "dist"
    $targetFullPath = [IO.Path]::GetFullPath($targetDist)
    $repoFullPath = [IO.Path]::GetFullPath($repo) + [IO.Path]::DirectorySeparatorChar
    if (-not $targetFullPath.StartsWith($repoFullPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to replace frontend files outside the Komari repository."
    }

    if (Test-Path -LiteralPath $targetDist) {
        Remove-Item -LiteralPath $targetDist -Recurse -Force
    }
    New-Item -ItemType Directory -Force -Path $targetDist | Out-Null
    Copy-Item -Path (Join-Path $webRepo "dist\*") -Destination $targetDist -Recurse -Force
    Copy-Item -LiteralPath (Join-Path $webRepo "komari-theme.json") -Destination $target -Force

    foreach ($name in @("preview.png", "perview.png")) {
        $source = Join-Path $webRepo $name
        if (Test-Path -LiteralPath $source) {
            Copy-Item -LiteralPath $source -Destination $target -Force
        }
    }
}

function Ensure-Frontend {
    if (-not (Test-Path -LiteralPath (Join-Path $repo "web\public\defaultTheme\dist\index.html"))) {
        Sync-Frontend
    }
}

switch ($Mode) {
    "web" {
        Push-Location $webRepo
        try {
            if (-not (Test-Path -LiteralPath (Join-Path $webRepo "node_modules"))) {
                & $npm install
            }
            & $npm run dev
        } finally {
            Pop-Location
        }
    }
    "build" {
        Sync-Frontend
        $outputDir = Join-Path $workspace "work\bin"
        New-Item -ItemType Directory -Force -Path $outputDir | Out-Null
        Push-Location $repo
        try {
            & $go build -p 1 -trimpath -o (Join-Path $outputDir "komari.exe") .
        } finally {
            Pop-Location
        }
    }
    "test" {
        Ensure-Frontend
        Push-Location $repo
        try {
            & $go test ./...
        } finally {
            Pop-Location
        }
    }
    "server" {
        Ensure-Frontend
        Push-Location $repo
        try {
            & $go run . server -l $Listen
        } finally {
            Pop-Location
        }
    }
}

if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
