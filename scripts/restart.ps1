param(
    [string]$Address = $(if ($env:APP_ADDRESS) { $env:APP_ADDRESS } else { "127.0.0.1:18781" }),
    [int]$StartupTimeoutSeconds = 30
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$runDirectory = Join-Path $projectRoot ".run"
$pidFile = Join-Path $runDirectory "powerlevel.pid"
$logFile = Join-Path $runDirectory "powerlevel.log"
$errorLogFile = Join-Path $runDirectory "powerlevel.error.log"
$binaryName = "powerlevel-server.exe"
$binaryPath = Join-Path $runDirectory $binaryName
$tempBinaryPath = Join-Path $runDirectory "powerlevel-server.next.exe"

function Get-ManagedProcess {
    if (-not (Test-Path $pidFile)) {
        return $null
    }

    $storedPid = 0
    if (-not [int]::TryParse((Get-Content $pidFile -Raw).Trim(), [ref]$storedPid)) {
        Remove-Item $pidFile -Force
        return $null
    }

    $process = Get-CimInstance Win32_Process -Filter "ProcessId = $storedPid" -ErrorAction SilentlyContinue
    if ($null -eq $process) {
        Remove-Item $pidFile -Force
        return $null
    }

    $actualPath = $process.ExecutablePath
    if ([string]::IsNullOrWhiteSpace($actualPath) -or
        -not [string]::Equals([IO.Path]::GetFullPath($actualPath), [IO.Path]::GetFullPath($binaryPath), [StringComparison]::OrdinalIgnoreCase)) {
        throw "The PID file does not point to this project's managed server. Refusing to stop PID $storedPid."
    }

    return $process
}

function Stop-ManagedProcess {
    $process = Get-ManagedProcess
    if ($null -eq $process) {
        return
    }

    Stop-Process -Id $process.ProcessId -ErrorAction Stop
    try {
        Wait-Process -Id $process.ProcessId -Timeout 20 -ErrorAction Stop
    } catch {
        Stop-Process -Id $process.ProcessId -Force -ErrorAction Stop
        Wait-Process -Id $process.ProcessId -Timeout 5 -ErrorAction SilentlyContinue
    }
    Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
}

function Get-HealthURL {
    $listenAddress = $Address.Trim()
    if ($listenAddress.StartsWith(":")) {
        return "http://127.0.0.1$listenAddress/healthz"
    }
    if ($listenAddress.StartsWith("0.0.0.0:")) {
        return "http://127.0.0.1:$($listenAddress.Substring(8))/healthz"
    }
    if ($listenAddress.StartsWith("[::]:")) {
        return "http://127.0.0.1:$($listenAddress.Substring(5))/healthz"
    }
    return "http://$listenAddress/healthz"
}

New-Item -ItemType Directory -Path $runDirectory -Force | Out-Null

Push-Location $projectRoot
try {
    & go build -o $tempBinaryPath ./cmd/server
    if ($LASTEXITCODE -ne 0) {
        throw "Go build failed. The old server is still running."
    }

    Stop-ManagedProcess
    # The old binary may still be locked briefly after Stop-ManagedProcess returns
    # (Windows releases the file handle asynchronously), so retry the overwrite a
    # few times before giving up.
    $moved = $false
    for ($i = 0; $i -lt 10; $i++) {
        try {
            Move-Item $tempBinaryPath $binaryPath -Force -ErrorAction Stop
            $moved = $true
            break
        } catch {
            Start-Sleep -Milliseconds 300
        }
    }
    if (-not $moved) {
        throw "Unable to replace the running server binary (file is still locked)."
    }

    # Set process-scoped env vars instead of Start-Process -Environment, which is
    # only available on PowerShell 7+; this script runs on Windows PowerShell 5.1.
    $previousAddress = $env:APP_ADDRESS
    $previousOpenBrowser = $env:POWERLEVEL_OPEN_BROWSER
    $env:APP_ADDRESS = $Address
    $env:POWERLEVEL_OPEN_BROWSER = "0"
    try {
        $process = Start-Process -FilePath $binaryPath `
            -WorkingDirectory $projectRoot `
            -RedirectStandardOutput $logFile `
            -RedirectStandardError $errorLogFile `
            -PassThru `
            -WindowStyle Hidden
    } finally {
        if ($null -eq $previousAddress) {
            Remove-Item Env:APP_ADDRESS -ErrorAction SilentlyContinue
        } else {
            $env:APP_ADDRESS = $previousAddress
        }
        if ($null -eq $previousOpenBrowser) {
            Remove-Item Env:POWERLEVEL_OPEN_BROWSER -ErrorAction SilentlyContinue
        } else {
            $env:POWERLEVEL_OPEN_BROWSER = $previousOpenBrowser
        }
    }
    Set-Content -Path $pidFile -Value $process.Id -NoNewline

    $healthURL = Get-HealthURL
    $deadline = (Get-Date).AddSeconds($StartupTimeoutSeconds)
    do {
        if ($process.HasExited) {
            Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
            throw "The new server exited early. Check $logFile and $errorLogFile."
        }
        try {
            $response = Invoke-WebRequest -Uri $healthURL -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200) {
                Write-Host "Server started: $healthURL (PID $($process.Id))"
                exit 0
            }
        } catch {
            Start-Sleep -Milliseconds 300
        }
    } while ((Get-Date) -lt $deadline)

    Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
    throw "The new server did not pass its health check within $StartupTimeoutSeconds seconds. Check $logFile and $errorLogFile."
} finally {
    Remove-Item $tempBinaryPath -Force -ErrorAction SilentlyContinue
    Pop-Location
}
