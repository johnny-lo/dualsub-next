# DualSub Next — daemon watcher.
#
# Starts dualsub.exe and automatically restarts it whenever
# %USERPROFILE%\.config\dualsub\config.toml changes on disk.
#
# Usage:
#   .\dualsub-watch.ps1
#
# Stop with Ctrl+C; the daemon child process is killed on exit.

$ErrorActionPreference = 'Stop'

$daemonExe  = Join-Path $PSScriptRoot 'dualsub.exe'
$configPath = Join-Path $env:USERPROFILE '.config\dualsub\config.toml'

if (-not (Test-Path $daemonExe)) {
  Write-Host "dualsub.exe not found at $daemonExe" -ForegroundColor Red
  Write-Host "Build it first:" -ForegroundColor Yellow
  Write-Host "  cd daemon ; go build -o ..\dualsub.exe .\cmd\dualsub" -ForegroundColor Yellow
  exit 1
}

if (-not (Test-Path $configPath)) {
  Write-Host "config not found at $configPath" -ForegroundColor Red
  Write-Host "Initialize it first: .\dualsub.exe config init" -ForegroundColor Yellow
  exit 1
}

function Start-Daemon {
  Write-Host "[watch] starting dualsub..." -ForegroundColor Cyan
  return Start-Process -FilePath $daemonExe -ArgumentList 'serve' -PassThru -NoNewWindow
}

function Stop-Daemon($p) {
  if ($p -and -not $p.HasExited) {
    Write-Host "[watch] stopping pid $($p.Id)..." -ForegroundColor Yellow
    Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
    $p.WaitForExit(3000) | Out-Null
  }
}

$proc = $null
$lastWrite = (Get-Item $configPath).LastWriteTimeUtc

try {
  $proc = Start-Daemon
  Write-Host "[watch] watching $configPath" -ForegroundColor Green
  Write-Host "[watch] press Ctrl+C to stop" -ForegroundColor Green

  while ($true) {
    Start-Sleep -Seconds 2

    if ($proc -and $proc.HasExited) {
      Write-Host "[watch] dualsub exited (code $($proc.ExitCode)) — bailing out" -ForegroundColor Red
      break
    }

    $lw = (Get-Item $configPath).LastWriteTimeUtc
    if ($lw -ne $lastWrite) {
      $lastWrite = $lw
      Write-Host "[watch] config.toml changed — restarting daemon" -ForegroundColor Magenta
      Stop-Daemon $proc
      Start-Sleep -Milliseconds 300
      $proc = Start-Daemon
    }
  }
} finally {
  Stop-Daemon $proc
  Write-Host "[watch] stopped" -ForegroundColor Cyan
}
