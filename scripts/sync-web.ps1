# 把桌面端 cmd/server/web 的前端资源同步到移动端 cmd/mobile/web。
# 两处前端文件（index.html / styles.css / app.js）必须保持一致：
# 移动端通过 gomobile 把 cmd/mobile 打成 AAR 内嵌 Web 资源，改动只在桌面端落地的话，
# 移动端 APK 会继续使用旧版前端。改动任何前端后请运行本脚本再提交。

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$src = Join-Path $projectRoot "cmd\server\web"
$dst = Join-Path $projectRoot "cmd\mobile\web"

$files = @("index.html", "styles.css", "app.js")

foreach ($file in $files) {
    $from = Join-Path $src $file
    $to = Join-Path $dst $file
    if (-not (Test-Path $from)) {
        throw "Missing source file: $from"
    }
    Copy-Item $from $to -Force
    Write-Host "synced $file"
}

Write-Host "Frontend synced: cmd/server/web -> cmd/mobile/web"