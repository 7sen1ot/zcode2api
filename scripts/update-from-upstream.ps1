param(
    [string]$Ref = "origin/main",
    [string]$PatchDirectory = "patches"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

$PatchRoot = Join-Path $Root $PatchDirectory
if (-not (Test-Path $PatchRoot)) {
    throw "Patch directory not found: $PatchRoot"
}
$Patches = @(Get-ChildItem $PatchRoot -Filter "*.patch" | Sort-Object Name)
if ($Patches.Count -eq 0) {
    throw "No patches found in $PatchRoot"
}
$dirty = @(git status --porcelain | Where-Object { $_ -notmatch '^\?\? (patches|scripts)/' })
if ($dirty.Count -gt 0) {
    throw "Working tree is not clean. Commit/stash changes before updating."
}

git fetch origin
$Upstream = (git rev-parse $Ref).Trim()
$Stamp = Get-Date -Format "yyyyMMdd-HHmmss"
$Worktree = Join-Path (Split-Path -Parent $Root) "zcode2api-upstream-$Stamp"
git worktree add --detach $Worktree $Upstream
if ($LASTEXITCODE -ne 0) {
    throw "Unable to create clean upstream worktree: $Worktree"
}

try {
    foreach ($Patch in $Patches) {
        git -C $Worktree apply --ignore-space-change --ignore-whitespace $Patch.FullName
        if ($LASTEXITCODE -ne 0) {
            throw "Patch failed against ${Upstream}: $($Patch.Name)"
        }
        Write-Host "Applied $($Patch.Name)."
    }
    $env:GOROOT = "D:\tools\go1.26.6\go"
    $env:GOPATH = "D:\tools\gopath"
    $env:GOMODCACHE = "D:\tools\gomodcache"
    $env:GOCACHE = "D:\tools\gocache"
    $env:GOPROXY = "https://goproxy.cn,direct"
    & "$env:GOROOT\bin\go.exe" -C $Worktree test ./internal/server
    if ($LASTEXITCODE -ne 0) {
        throw "zcode2api tests failed after applying patch"
    }
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    New-Item -ItemType Directory -Force (Join-Path $Worktree "bin") | Out-Null
    $BuildVersion = (git -C $Worktree describe --tags --abbrev=0 --match "v*").Trim()
    if (-not $BuildVersion) {
        throw "No release tag found for version injection"
    }
    & "$env:GOROOT\bin\go.exe" -C $Worktree build -trimpath -ldflags "-X main.version=$BuildVersion" -o bin/zcode2api-linux-amd64 .
    if ($LASTEXITCODE -ne 0) {
        throw "zcode2api Linux build failed"
    }
    Copy-Item -Force (Join-Path $Worktree "bin/zcode2api-linux-amd64") (Join-Path $Root "bin/zcode2api-linux-amd64")
    Write-Host "Built patched upstream $Upstream ($BuildVersion)."
} finally {
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    git worktree remove --force $Worktree 2>$null
}
Write-Host "Deploy with scripts/deploy-blue-green.ps1, then switch traffic after health checks."
