# Build and run helper for the xiangqi server and web client.
#
# This machine has no HTTPS egress: TLS handshakes fail, so npm and the Go
# module proxy must be reached over plain HTTP, and their caches must live
# inside the workspace because the default cache locations are outside it.
# This script centralises that environment so every command behaves the same.

[CmdletBinding()]
param(
    [ValidateSet('env', 'deps', 'web', 'server', 'test', 'all', 'run')]
    [string]$Task = 'all',

    # Address the server binds to when -Task run is used.
    [string]$Addr = ':8080',

    # Serve the built frontend from this directory.
    [string]$PublicDir = 'web/dist'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not (Test-Path (Join-Path $root 'server'))) {
    throw "Cannot find the server directory under $root"
}

function Initialize-BuildEnv {
    param([string]$Root)

    $cache = Join-Path $Root '.cache'
    foreach ($d in 'go-build', 'go-mod', 'npm') {
        New-Item -ItemType Directory -Force -Path (Join-Path $cache $d) | Out-Null
    }

    # Go: module proxy over HTTPS. The checksum database stays enabled —
    # goproxy.cn proxies sum.golang.org, so verification works without direct
    # egress. Turning it off would let a first download be swapped silently.
    $env:GOCACHE = Join-Path $cache 'go-build'
    $env:GOMODCACHE = Join-Path $cache 'go-mod'
    if (-not $env:GOPROXY) { $env:GOPROXY = 'https://goproxy.cn,direct' }
    $env:GOTOOLCHAIN = 'local'
    $env:GOFLAGS = '-mod=mod'

    # npm: registry over HTTPS and a workspace-local cache.
    if (-not $env:npm_config_registry) { $env:npm_config_registry = 'https://registry.npmmirror.com' }
    $env:npm_config_cache = Join-Path $cache 'npm'
    $env:npm_config_fund = 'false'
    $env:npm_config_audit = 'false'
    $env:npm_config_update_notifier = 'false'

    Write-Host "GOCACHE      = $env:GOCACHE"
    Write-Host "GOMODCACHE   = $env:GOMODCACHE"
    Write-Host "GOPROXY      = $env:GOPROXY"
    Write-Host "npm registry = $env:npm_config_registry"
    Write-Host "npm cache    = $env:npm_config_cache"
}

function Invoke-GoDeps {
    param([string]$Root)
    Push-Location (Join-Path $Root 'server')
    try {
        Write-Host '==> go mod download' -ForegroundColor Cyan
        & go mod download
        if ($LASTEXITCODE -ne 0) { throw "go mod download failed with $LASTEXITCODE" }
    }
    finally { Pop-Location }
}

function Invoke-WebBuild {
    param([string]$Root)
    $web = Join-Path $Root 'web'
    if (-not (Test-Path (Join-Path $web 'package.json'))) {
        throw "No web/package.json found; the frontend has not been created yet"
    }
    Push-Location $web
    try {
        # --ignore-scripts: npm's own rebuild step spawns lifecycle scripts over
        # stdio pipes, which the DSH file sandbox refuses (spawn EPERM). No
        # dependency here needs a local compile: esbuild and rollup ship their
        # platform binaries inside optional packages.
        Write-Host '==> npm install --ignore-scripts' -ForegroundColor Cyan
        & npm install --ignore-scripts --no-audit --no-fund
        if ($LASTEXITCODE -ne 0) { throw "npm install failed with $LASTEXITCODE" }

        # npm run build goes through web/scripts/vite.mjs, which probes esbuild
        # and falls back to an in-process Rollup build when esbuild cannot spawn.
        Write-Host '==> npm run build' -ForegroundColor Cyan
        & npm run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build failed with $LASTEXITCODE" }
    }
    finally { Pop-Location }
}

function Invoke-ServerBuild {
    param([string]$Root, [string]$OutDir)
    Push-Location (Join-Path $Root 'server')
    try {
        New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
        Write-Host '==> go build ./cmd/server' -ForegroundColor Cyan
        & go build -o (Join-Path $OutDir 'xiangqi-server.exe') ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw "go build failed with $LASTEXITCODE" }
        Write-Host "built $(Join-Path $OutDir 'xiangqi-server.exe')"
    }
    finally { Pop-Location }
}

function Invoke-Tests {
    param([string]$Root)
    Push-Location (Join-Path $Root 'server')
    try {
        Write-Host '==> go vet ./...' -ForegroundColor Cyan
        & go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed with $LASTEXITCODE" }

        Write-Host '==> go test ./...' -ForegroundColor Cyan
        & go test ./... -count=1
        if ($LASTEXITCODE -ne 0) { throw "go test failed with $LASTEXITCODE" }
    }
    finally { Pop-Location }
}

Initialize-BuildEnv -Root $root

switch ($Task) {
    'env' { }
    'deps' { Invoke-GoDeps -Root $root }
    'web' { Invoke-WebBuild -Root $root }
    'server' { Invoke-GoDeps -Root $root; Invoke-ServerBuild -Root $root -OutDir (Join-Path $root 'bin') }
    'test' { Invoke-Tests -Root $root }
    'all' {
        Invoke-GoDeps -Root $root
        Invoke-WebBuild -Root $root
        Invoke-Tests -Root $root
        Invoke-ServerBuild -Root $root -OutDir (Join-Path $root 'bin')
    }
    'run' {
        Invoke-GoDeps -Root $root
        $env:XIANGQI_ADDR = $Addr
        $env:XIANGQI_PUBLIC_DIR = (Join-Path $root $PublicDir)
        Write-Host "==> starting server on $Addr (static: $env:XIANGQI_PUBLIC_DIR)" -ForegroundColor Cyan
        Push-Location (Join-Path $root 'server')
        try { & go run ./cmd/server } finally { Pop-Location }
    }
}
