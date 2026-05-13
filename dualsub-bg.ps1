# DualSub Next background daemon helper for Windows.
#
# Usage:
#   .\dualsub-bg.ps1 start
#   .\dualsub-bg.ps1 stop
#   .\dualsub-bg.ps1 restart
#   .\dualsub-bg.ps1 status
#   .\dualsub-bg.ps1 install-startup
#   .\dualsub-bg.ps1 uninstall-startup

param(
  [ValidateSet('start', 'stop', 'restart', 'status', 'install-startup', 'uninstall-startup')]
  [string]$Action = 'start'
)

$ErrorActionPreference = 'Stop'

$daemonExe = Join-Path $PSScriptRoot 'dualsub.exe'
$stateDir = Join-Path $env:LOCALAPPDATA 'DualSub Next'
$pidPath = Join-Path $stateDir 'dualsub.pid'
$stdoutPath = Join-Path $stateDir 'dualsub.stdout.log'
$stderrPath = Join-Path $stateDir 'dualsub.stderr.log'
$healthUrl = 'http://127.0.0.1:7878/healthz'
$startupScriptName = 'DualSub Next Daemon.vbs'

function Test-DualSubHealth {
  try {
    $res = Invoke-RestMethod -Uri $healthUrl -TimeoutSec 2
    return $res.status -eq 'ok'
  } catch {
    return $false
  }
}

function Get-TrackedProcess {
  if (-not (Test-Path $pidPath)) {
    return $null
  }

  $raw = (Get-Content $pidPath -ErrorAction SilentlyContinue | Select-Object -First 1)
  $pidValue = 0
  if (-not [int]::TryParse($raw, [ref]$pidValue)) {
    return $null
  }

  return Get-Process -Id $pidValue -ErrorAction SilentlyContinue
}

function Get-DualSubProcesses {
  if (-not (Test-Path $daemonExe)) {
    return @()
  }

  $expected = (Resolve-Path $daemonExe).Path
  return @(Get-CimInstance Win32_Process -Filter "name = 'dualsub.exe'" |
    Where-Object { $_.ExecutablePath -eq $expected })
}

function Start-DualSub {
  if (Test-DualSubHealth) {
    Write-Host "DualSub daemon is already running at $healthUrl"
    return
  }

  if (-not (Test-Path $daemonExe)) {
    Write-Host "dualsub.exe not found at $daemonExe" -ForegroundColor Red
    Write-Host "Build it first: cd daemon ; go build -o ..\dualsub.exe .\cmd\dualsub"
    exit 1
  }

  New-Item -ItemType Directory -Path $stateDir -Force | Out-Null
  Remove-Item $pidPath -Force -ErrorAction SilentlyContinue

  $proc = Start-Process `
    -FilePath $daemonExe `
    -ArgumentList @('serve') `
    -WindowStyle Hidden `
    -RedirectStandardOutput $stdoutPath `
    -RedirectStandardError $stderrPath `
    -PassThru

  Set-Content -Path $pidPath -Value $proc.Id -Encoding ASCII
  Start-Sleep -Seconds 1

  if (Test-DualSubHealth) {
    Write-Host "DualSub daemon started in background. PID: $($proc.Id)"
  } else {
    Write-Host "DualSub daemon process started, but health check is not ready yet. PID: $($proc.Id)" -ForegroundColor Yellow
  }
  Write-Host "stdout: $stdoutPath"
  Write-Host "stderr: $stderrPath"
}

function Stop-DualSub {
  $stopped = $false
  $tracked = Get-TrackedProcess
  if ($tracked -and -not $tracked.HasExited) {
    Stop-Process -Id $tracked.Id -Force
    $stopped = $true
  }

  foreach ($proc in Get-DualSubProcesses) {
    Stop-Process -Id $proc.ProcessId -Force -ErrorAction SilentlyContinue
    $stopped = $true
  }

  Remove-Item $pidPath -Force -ErrorAction SilentlyContinue

  if ($stopped) {
    Write-Host 'DualSub daemon stopped.'
  } else {
    Write-Host 'DualSub daemon is not running.'
  }
}

function Show-Status {
  $tracked = Get-TrackedProcess
  if (Test-DualSubHealth) {
    if ($tracked) {
      Write-Host "DualSub daemon is running. PID: $($tracked.Id)"
    } else {
      Write-Host 'DualSub daemon is running, but no PID file is available.'
    }
    return
  }

  if ($tracked) {
    Write-Host "DualSub daemon process exists, but health check failed. PID: $($tracked.Id)" -ForegroundColor Yellow
    return
  }

  Write-Host 'DualSub daemon is offline.'
}

function Install-Startup {
  $startupDir = [Environment]::GetFolderPath('Startup')
  $target = Join-Path $startupDir $startupScriptName
  $scriptPath = $PSCommandPath
  $command = 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "' + $scriptPath + '" -Action start'
  $content = @(
    'Set shell = CreateObject("WScript.Shell")'
    'shell.Run "' + $command.Replace('"', '""') + '", 0, False'
  )
  Set-Content -Path $target -Value $content -Encoding ASCII
  Write-Host "Installed startup launcher: $target"
}

function Uninstall-Startup {
  $startupDir = [Environment]::GetFolderPath('Startup')
  $target = Join-Path $startupDir $startupScriptName
  Remove-Item $target -Force -ErrorAction SilentlyContinue
  Write-Host "Removed startup launcher: $target"
}

switch ($Action) {
  'start' { Start-DualSub }
  'stop' { Stop-DualSub }
  'restart' { Stop-DualSub; Start-Sleep -Milliseconds 300; Start-DualSub }
  'status' { Show-Status }
  'install-startup' { Install-Startup }
  'uninstall-startup' { Uninstall-Startup }
}
