$ErrorActionPreference = "Stop"
$appRoot = Split-Path -Parent $PSScriptRoot
$binaryPath = Join-Path $appRoot "PowerLevel.exe"
$runtimeRoot = Join-Path $appRoot "runtime"
$pidFile = Join-Path $runtimeRoot "powerlevel.pid"
$logFile = Join-Path $runtimeRoot "powerlevel.log"
$errorLogFile = Join-Path $runtimeRoot "powerlevel.error.log"
$configFile = Join-Path $appRoot "config.env"
$configExample = Join-Path $appRoot "config.env.example"
$allowedConfig = @("APP_ADDRESS", "REQUEST_TIMEOUT", "PROVIDER_TIMEOUT", "CACHE_TTL", "PARTIAL_CACHE_TTL", "CACHE_MAX_ENTRIES", "EDH_MAX_CONCURRENCY", "BROWSER_PATH", "BROWSER_HEADLESS")

function Find-Browser {
    $candidates = @(
        $env:BROWSER_PATH,
        "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
        "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
        "$env:LOCALAPPDATA\Microsoft\Edge\Application\msedge.exe",
        "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
        "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe"
    )
    foreach ($candidate in $candidates) {
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and (Test-Path $candidate -PathType Leaf)) { return $candidate }
    }
    return $null
}

function Get-ManagedProcess {
    if (-not (Test-Path $pidFile)) { return $null }
    $storedPid = 0
    if (-not [int]::TryParse((Get-Content $pidFile -Raw).Trim(), [ref]$storedPid)) { Remove-Item $pidFile -Force; return $null }
    $process = Get-CimInstance Win32_Process -Filter "ProcessId = $storedPid" -ErrorAction SilentlyContinue
    if ($null -eq $process) { Remove-Item $pidFile -Force; return $null }
    if (-not [string]::Equals([IO.Path]::GetFullPath($process.ExecutablePath), [IO.Path]::GetFullPath($binaryPath), [StringComparison]::OrdinalIgnoreCase)) {
        throw "PowerLevel operation failed."
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

# 找不到二进制时，尝试从源码在本地构建。这样把仓库 clone 下来后，
# 只要机器上装过 Go，双击启动脚本也能一键跑起来。
if (-not (Test-Path $binaryPath -PathType Leaf)) {
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $goCmd) { throw "PowerLevel operation failed: no binary and Go is not available." }
    Push-Location $appRoot
    try {
        & go build -o $binaryPath ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw "PowerLevel operation failed: go build failed." }
    } finally {
        Pop-Location
    }
}
if (-not (Test-Path $binaryPath -PathType Leaf)) {
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $goCmd) { throw "PowerLevel operation failed: no binary and Go is not available." }
    Push-Location $appRoot
    try {
        & go build -o $binaryPath ./cmd/server
        if ($LASTEXITCODE -ne 0) { throw "PowerLevel operation failed: go build failed." }
    } finally {
        Pop-Location
    }
}
if (-not (Test-Path $binaryPath -PathType Leaf)) { throw "PowerLevel operation failed." }
New-Item -ItemType Directory -Path $runtimeRoot -Force | Out-Null
if (-not (Test-Path $configFile) -and (Test-Path $configExample)) { Copy-Item $configExample $configFile }
if (Test-Path $configFile) {
    foreach ($line in Get-Content $configFile) {
        $line = $line.Trim()
        if ($line -eq "" -or $line.StartsWith("#") -or -not $line.Contains("=")) { continue }
        $parts = $line.Split("=", 2); $key = $parts[0].Trim(); $value = $parts[1].Trim()
        if ($allowedConfig -contains $key) { Set-Item -Path "Env:$key" -Value $value }
    }
}
$browserPath = Find-Browser
if (-not $browserPath) { throw "PowerLevel operation failed." }
$env:BROWSER_PATH = $browserPath

$managed = Get-ManagedProcess
$healthURL = Get-HealthURL
$appURL = $healthURL.Substring(0, $healthURL.Length - "/healthz".Length)
if ($managed) {
    try { $response = Invoke-WebRequest -Uri $healthURL -UseBasicParsing -TimeoutSec 2; if ($response.StatusCode -eq 200) { Start-Process $appURL; Write-Host "PowerLevel status updated."; exit 0 } } catch {}
    Stop-Process -Id $managed.ProcessId -Force -ErrorAction SilentlyContinue
    Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
    $managed = $null
}

$process = Start-Process -FilePath $binaryPath -WorkingDirectory $appRoot -RedirectStandardOutput $logFile -RedirectStandardError $errorLogFile -PassThru -WindowStyle Hidden -Environment @{ POWERLEVEL_OPEN_BROWSER = "0" }
Set-Content -Path $pidFile -Value $process.Id -NoNewline
$deadline = (Get-Date).AddSeconds(35)
do {
    if ($process.HasExited) { Remove-Item $pidFile -Force -ErrorAction SilentlyContinue; if (Test-Path $errorLogFile) { Start-Process notepad.exe $errorLogFile }; throw "PowerLevel operation failed." }
    try {
        $response = Invoke-WebRequest -Uri $healthURL -UseBasicParsing -TimeoutSec 2
        if ($response.StatusCode -eq 200) { Start-Process $appURL; Write-Host "PowerLevel status updated."; exit 0 }
    } catch { Start-Sleep -Milliseconds 350 }
} while ((Get-Date) -lt $deadline)
Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
if (Test-Path $errorLogFile) { Start-Process notepad.exe $errorLogFile }
throw "PowerLevel operation failed."
