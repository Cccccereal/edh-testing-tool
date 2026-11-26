param(
    [switch]$Stop
)

$ErrorActionPreference = "Stop"

$projectRoot = $PSScriptRoot
$runDirectory = Join-Path $projectRoot ".run"
$binaryPath = Join-Path $runDirectory "powerlevel-server.exe"
$pidFile = Join-Path $runDirectory "powerlevel.pid"
$logFile = Join-Path $runDirectory "powerlevel.log"
$errorLogFile = Join-Path $runDirectory "powerlevel.error.log"

function Get-ManagedProcess {
    if (-not (Test-Path $pidFile)) { return $null }
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
    if ([string]::IsNullOrWhiteSpace($process.ExecutablePath) -or
        -not [string]::Equals([IO.Path]::GetFullPath($process.ExecutablePath), [IO.Path]::GetFullPath($binaryPath), [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to stop another process. PID file does not point to this server."
    }
    return $process
}

function Get-HealthURL {
    $address = if ($env:APP_ADDRESS) { $env:APP_ADDRESS.Trim() } else { "127.0.0.1:18781" }
    if ($address.StartsWith(":")) { return "http://127.0.0.1$address/healthz" }
    if ($address.StartsWith("0.0.0.0:")) { return "http://127.0.0.1:$($address.Substring(8))/healthz" }
    if ($address.StartsWith("[::]:")) { return "http://127.0.0.1:$($address.Substring(5))/healthz" }
    return "http://$address/healthz"
}

New-Item -ItemType Directory -Path $runDirectory -Force | Out-Null

if ($Stop) {
    $managed = Get-ManagedProcess
    if ($null -eq $managed) {
        Write-Host "No running server found."
        exit 0
    }
    Stop-Process -Id $managed.ProcessId -ErrorAction Stop
    try { Wait-Process -Id $managed.ProcessId -Timeout 20 -ErrorAction Stop }
    catch { Stop-Process -Id $managed.ProcessId -Force -ErrorAction Stop; Wait-Process -Id $managed.ProcessId -Timeout 5 -ErrorAction SilentlyContinue }
    Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
    Write-Host "Server stopped."
    exit 0
}

$managed = Get-ManagedProcess
$healthURL = Get-HealthURL
$appURL = $healthURL.Substring(0, $healthURL.Length - "/healthz".Length)

if ($null -ne $managed) {
    try {
        $response = Invoke-WebRequest -Uri $healthURL -UseBasicParsing -TimeoutSec 2
        if ($response.StatusCode -eq 200) {
            Start-Process $appURL
            exit 0
        }
    } catch {}
    Stop-Process -Id $managed.ProcessId -Force -ErrorAction SilentlyContinue
    Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
}

if (-not (Test-Path $binaryPath -PathType Leaf)) {
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $goCmd) {
        Write-Host "No prebuilt server binary and Go is not installed."
        Write-Host "Run scripts\build-release.ps1 to build a release, or install Go and retry."
        Read-Host "Press Enter to exit"
        exit 1
    }
    Write-Host "Building the server on first run (this may take a moment)..."
    Push-Location $projectRoot
    try {
        & go build -o $binaryPath ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw "Go build failed." }
    } finally {
        Pop-Location
    }
}

# PowerShell 5.1 lacks `Start-Process -Environment`, so we set the variable in the
# current scope and let the child inherit it. POWERLEVEL_OPEN_BROWSER=0 keeps the
# server from popping the browser itself; the launcher opens it once healthy.
$previousOpenBrowser = $env:POWERLEVEL_OPEN_BROWSER
$env:POWERLEVEL_OPEN_BROWSER = "0"
try {
    $process = Start-Process -FilePath $binaryPath `
        -WorkingDirectory $projectRoot `
        -RedirectStandardOutput $logFile `
        -RedirectStandardError $errorLogFile `
        -PassThru `
        -WindowStyle Hidden
} finally {
    $env:POWERLEVEL_OPEN_BROWSER = $previousOpenBrowser
}
Set-Content -Path $pidFile -Value $process.Id -NoNewline

$deadline = (Get-Date).AddSeconds(35)
do {
    if ($process.HasExited) {
        Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
        if (Test-Path $errorLogFile) { Get-Content $errorLogFile | Select-Object -Last 20 }
        Write-Host "Server failed to start. See .run\powerlevel.error.log."
        Read-Host "Press Enter to exit"
        exit 1
    }
    try {
        $response = Invoke-WebRequest -Uri $healthURL -UseBasicParsing -TimeoutSec 2
        if ($response.StatusCode -eq 200) {
            Start-Process $appURL
            exit 0
        }
    } catch { Start-Sleep -Milliseconds 350 }
} while ((Get-Date) -lt $deadline)

Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
Write-Host "Server did not become ready in time. See .run\powerlevel.error.log."
Read-Host "Press Enter to exit"
exit 1
