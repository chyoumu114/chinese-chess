# End-to-end verification of the whole project.
#
# This script is the acceptance check: it builds the server, runs every Go test,
# checks the frontend build output, starts the server on a free port with the
# in-memory store, and drives a real two-player game over WebSocket.
#
#   pwsh -File scripts/verify.ps1
#
# It needs no database. Exit code 0 means everything passed.

[CmdletBinding()]
param(
    # Port for the temporary server. 0 picks a random free port.
    [int]$Port = 0,

    # Keep the server running after the checks, on the chosen port.
    [switch]$KeepRunning
)

$ErrorActionPreference = 'Stop'
# Run from this script's own directory so it works from anywhere.
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Write-Step($text) { Write-Host "`n=== $text ===" -ForegroundColor Cyan }
function Write-Ok($text) { Write-Host "  OK   $text" -ForegroundColor Green }
function Write-Bad($text) { Write-Host "  FAIL $text" -ForegroundColor Red }

$failures = New-Object System.Collections.Generic.List[string]

function Check($condition, $description) {
    if ($condition) { Write-Ok $description } else { Write-Bad $description; $failures.Add($description) }
}

# ---- environment -----------------------------------------------------------
# The default caches live outside the workspace, so they are redirected here.
# Proxy and registry use HTTPS; see scripts/build.ps1 for the details.
$cache = Join-Path $root '.cache'
foreach ($d in 'go-build', 'go-mod', 'npm') {
    New-Item -ItemType Directory -Force -Path (Join-Path $cache $d) | Out-Null
}
$env:GOCACHE = Join-Path $cache 'go-build'
$env:GOMODCACHE = Join-Path $cache 'go-mod'
if (-not $env:GOPROXY) { $env:GOPROXY = 'https://goproxy.cn,direct' }
$env:GOTOOLCHAIN = 'local'
$env:GOFLAGS = '-mod=mod'

# ---- 1. repository layout --------------------------------------------------
Write-Step 'Repository layout'
$expected = @(
    'README.md',
    'server/go.mod',
    'server/cmd/server/main.go',
    'server/cmd/e2e/main.go',
    'server/internal/game/board.go',
    'server/internal/game/moves.go',
    'server/internal/game/game.go',
    'server/internal/gamehall/session.go',
    'server/internal/ws/hub.go',
    'server/internal/api/server.go',
    'server/internal/auth/password.go',
    'server/internal/store/schema.go',
    'server/internal/ai/ai.go',
    'server/internal/config/config.go',
    'docs/frontend-contract.md',
    'deploy/docker-compose.yml',
    'deploy/Dockerfile',
    'scripts/build.ps1'
)
foreach ($rel in $expected) {
    Check (Test-Path (Join-Path $root $rel)) $rel
}

# ---- 2. Go build, vet and tests -------------------------------------------
Write-Step 'Go build, vet and tests'
Push-Location (Join-Path $root 'server')
try {
    & go build ./... 2>&1 | Out-Host
    Check ($LASTEXITCODE -eq 0) 'go build ./...'

    & go vet ./... 2>&1 | Out-Host
    Check ($LASTEXITCODE -eq 0) 'go vet ./...'

    $testOutput = & go test ./... -count=1 -timeout 300s 2>&1
    $testOutput | Out-Host
    Check ($LASTEXITCODE -eq 0) 'go test ./...'

    # Report which packages actually ran tests, so a package that silently has no
    # coverage is visible rather than implied.
    $ran = ($testOutput | Select-String -Pattern '^ok\s+(\S+)' -AllMatches).Matches |
        ForEach-Object { $_.Groups[1].Value }
    Write-Host ("  packages with passing tests: " + ($ran -join ', '))
}
finally { Pop-Location }

# ---- 3. frontend build output ---------------------------------------------
Write-Step 'Frontend build output'
$dist = Join-Path $root 'web/dist'
$hasDist = Test-Path (Join-Path $dist 'index.html')
Check $hasDist 'web/dist/index.html exists'
if ($hasDist) {
    $assets = Get-ChildItem (Join-Path $dist 'assets') -File -ErrorAction SilentlyContinue
    Check ($assets.Count -gt 0) ("web/dist/assets contains $($assets.Count) files")

    $index = Get-Content (Join-Path $dist 'index.html') -Raw -Encoding UTF8
    Check ($index -match '<div id="app">') 'the shell contains the app mount point'
    Check ($index -match 'type="module"') 'the shell loads the bundle as a module'

    # The Chinese UI and the piece glyphs must survive the build. Windows
    # PowerShell reads files as ANSI by default, which mangles UTF-8, so the
    # encoding must be stated explicitly or every check below would fail.
    $utf8 = New-Object System.Text.UTF8Encoding($false)
    $joined = ''
    foreach ($file in (Get-ChildItem $dist -Recurse -Filter '*.js' -File)) {
        $joined += $utf8.GetString([System.IO.File]::ReadAllBytes($file.FullName))
    }
    foreach ($needle in '快速匹配', '楚河', '汉界', '认输', '提和', '悔棋', '将死') {
        Check ($joined.Contains($needle)) "bundle contains the UI string $needle"
    }
    foreach ($glyph in '帅', '将', '车', '炮', '兵', '卒') {
        Check ($joined.Contains($glyph)) "bundle contains the piece glyph $glyph"
    }

    # Frontend unit tests (board perspective mapping, vite define rewriting).
    #
    # The test files are passed to node one at a time instead of using
    # `node --test`: the latter forks a child process per file, and the DSH file
    # sandbox refuses child processes with piped stdio (spawn EPERM), which would
    # fail here while passing on an unrestricted machine.
    $webTests = @('scripts/defines.test.mjs', 'scripts/board.test.mjs')
    if (Test-Path (Join-Path $root 'web/node_modules')) {
        Push-Location (Join-Path $root 'web')
        try {
            foreach ($rel in $webTests) {
                & node $rel 2>&1 | Out-Host
                Check ($LASTEXITCODE -eq 0) "web unit test $rel"
            }
        }
        finally { Pop-Location }
    } else {
        Write-Host "  SKIP web unit tests (web/node_modules is missing; run npm install)" -ForegroundColor Yellow
    }
}

# ---- 4. live server + real game -------------------------------------------
Write-Step 'Live server and a real two-player game'

# Pick a free port when none was requested.
if ($Port -eq 0) {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $Port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
}
$base = "http://127.0.0.1:$Port"

$serverEnv = @{
    XIANGQI_ADDR          = "127.0.0.1:$Port"
    XIANGQI_PUBLIC_DIR    = $dist
    XIANGQI_JWT_SECRET    = 'verify-script-secret-0123456789'
    XIANGQI_ENV           = 'development'
    XIANGQI_TURN_TIMEOUT  = '30s'
    XIANGQI_GAME_TIMEOUT  = '5m'
}

# Start the server as a child process so it can be stopped reliably.
$exe = Join-Path $root 'bin/xiangqi-server.exe'
Write-Host "  building $exe"
New-Item -ItemType Directory -Force -Path (Join-Path $root 'bin') | Out-Null
Push-Location (Join-Path $root 'server')
try {
    & go build -o $exe ./cmd/server 2>&1 | Out-Host
    Check ($LASTEXITCODE -eq 0) 'go build ./cmd/server'
}
finally { Pop-Location }

$originalEnv = @{}
foreach ($k in $serverEnv.Keys) {
    $originalEnv[$k] = [Environment]::GetEnvironmentVariable($k, 'Process')
    [Environment]::SetEnvironmentVariable($k, $serverEnv[$k], 'Process')
}

$server = Start-Process -FilePath $exe -ArgumentList '-store', 'memory', '-log-level', 'warn' `
    -PassThru -NoNewWindow -RedirectStandardOutput (Join-Path $root 'bin/server.log') `
    -RedirectStandardError (Join-Path $root 'bin/server.err.log')

try {
    # Wait for the health endpoint to answer.
    $ready = $false
    for ($i = 0; $i -lt 40; $i++) {
        Start-Sleep -Milliseconds 500
        try {
            $r = Invoke-WebRequest -Uri "$base/api/health" -TimeoutSec 3 -UseBasicParsing
            if ($r.StatusCode -eq 200) { $ready = $true; break }
        } catch { }
    }
    Check $ready "server answered /api/health on $base"

    if ($ready) {
        # The API and the single-page app must coexist.
        $spaRoutes = @('/', '/lobby', '/game/1', '/history', '/leaderboard', '/profile')
        foreach ($route in $spaRoutes) {
            try {
                $r = Invoke-WebRequest -Uri "$base$route" -TimeoutSec 5 -UseBasicParsing
                Check ($r.StatusCode -eq 200 -and $r.Content -match '<div id="app">') "SPA route $route"
            } catch {
                Check $false "SPA route $route ($($_.Exception.Message))"
            }
        }
        try {
            $r = Invoke-WebRequest -Uri "$base/api/does-not-exist" -TimeoutSec 5 -UseBasicParsing
            Check $false "unknown API path should not be 200"
        } catch {
            $body = $_.ErrorDetails.Message
            Check ($body -match 'not_found') 'unknown API path returns a JSON not_found error'
        }

        # Drive a real game through the e2e program.
        Push-Location (Join-Path $root 'server')
        try {
            $e2e = & go run ./cmd/e2e -base $base 2>&1
            $e2e | Out-Host
            Check ($LASTEXITCODE -eq 0) 'end-to-end game verification (cmd/e2e)'
        }
        finally { Pop-Location }
    }
}
finally {
    if (-not $KeepRunning) {
        if ($server -and -not $server.HasExited) {
            Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
        }
    } else {
        Write-Host "`n  server left running on $base (pid $($server.Id))" -ForegroundColor Yellow
    }
    foreach ($k in $originalEnv.Keys) {
        [Environment]::SetEnvironmentVariable($k, $originalEnv[$k], 'Process')
    }
}

# ---- summary ---------------------------------------------------------------
Write-Step 'Summary'
if ($failures.Count -eq 0) {
    Write-Host "ALL CHECKS PASSED" -ForegroundColor Green
    exit 0
}
Write-Host "$($failures.Count) CHECK(S) FAILED:" -ForegroundColor Red
foreach ($f in $failures) { Write-Host "  - $f" -ForegroundColor Red }
exit 1
