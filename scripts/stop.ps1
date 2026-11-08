$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$sourceCheckout = Test-Path -LiteralPath (Join-Path $projectRoot 'go.mod') -PathType Leaf
$binaryPath = if ($sourceCheckout) { Join-Path $projectRoot 'bin/zcode2api.exe' } else { Join-Path $projectRoot 'zcode2api.exe' }
$dataPath = if ($env:ZCODE2API_DATA_DIR) {
    $configuredDataPath = $env:ZCODE2API_DATA_DIR
    if (![IO.Path]::IsPathRooted($configuredDataPath)) { $configuredDataPath = Join-Path $projectRoot $configuredDataPath }
    [IO.Path]::GetFullPath($configuredDataPath)
} else { Join-Path $projectRoot 'data' }
$portsFile = Join-Path $dataPath 'ports.json'
if (!(Test-Path -LiteralPath $portsFile)) { Write-Output 'No ports.json; nothing to stop.'; exit 0 }
$info = Get-Content -LiteralPath $portsFile -Raw | ConvertFrom-Json
$serviceProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $($info.pid)" -ErrorAction SilentlyContinue
if (!$serviceProcess) { Write-Output 'Already stopped.'; exit 0 }
$isGoService = $serviceProcess.Name -eq 'zcode2api.exe' -and $serviceProcess.ExecutablePath -eq $binaryPath
if (!$isGoService) { throw 'PID does not match this project; refusing to stop an unrelated process.' }
Stop-Process -Id $info.pid
Write-Output "Stopped PID $($info.pid)"
