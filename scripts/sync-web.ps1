# 把桌面端 cmd/server/web 的前端资源同步到移动端 cmd/mobile/web。
# 两处前端必须严格一致（含 fonts/ 子目录）：移动端通过 gomobile 把 cmd/mobile
# 打成 AAR 内嵌 Web 资源（go:embed web/* 会递归打包子目录），改动只在桌面端
# 落地的话，移动端 APK 会继续使用旧版前端——自托管标题字体就曾只进了桌面端，
# 安卓端一直 404。改动任何前端后请运行本脚本再提交。

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$src = Join-Path $projectRoot "cmd\server\web"
$dst = Join-Path $projectRoot "cmd\mobile\web"

if (-not (Test-Path $src)) {
    throw "Missing source dir: $src"
}

# 全量镜像：复制/更新所有条目（文件与目录），并删除目标里源里已没有的条目，
# 保持严格镜像（webmirror_test.go 也按此断言）。
Get-ChildItem -LiteralPath $src | ForEach-Object {
    $to = Join-Path $dst $_.Name
    if (Test-Path $to) { Remove-Item -Recurse -Force $to }
    Copy-Item $_.FullName $to -Recurse -Force
    Write-Host "synced $($_.Name)"
}

Get-ChildItem -LiteralPath $dst | ForEach-Object {
    if (-not (Test-Path (Join-Path $src $_.Name))) {
        Remove-Item -Recurse -Force $_.FullName
        Write-Host "removed stale $($_.Name)"
    }
}

Write-Host "Frontend synced: cmd/server/web -> cmd/mobile/web"
