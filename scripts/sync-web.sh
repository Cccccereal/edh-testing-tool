#!/bin/bash
# 把桌面端 cmd/server/web 的前端资源同步到移动端 cmd/mobile/web。
# 两处前端必须严格一致（含 fonts/ 子目录）：移动端通过 gomobile 把 cmd/mobile
# 打成 AAR 内嵌 Web 资源（go:embed web/* 会递归打包子目录），改动只在桌面端
# 落地的话，移动端 APK 会继续使用旧版前端——自托管标题字体就曾只进了桌面端，
# 安卓端一直 404。改动任何前端后请运行本脚本再提交；CI 用
# `go test ./cmd/mobile -run TestWebMirrorMatchesServerWeb` 做递归一致性把关。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
SRC="$PROJECT_ROOT/cmd/server/web"
DST="$PROJECT_ROOT/cmd/mobile/web"

if [ ! -d "$SRC" ]; then
    echo "Error: Missing source dir: $SRC" >&2
    exit 1
fi

# 全量镜像：复制/更新所有条目（文件与目录），并删除目标里源里已没有的条目，
# 保持严格镜像（webmirror_test.go 也按此断言）。
found_any=false
for entry in "$SRC"/*; do
    name="$(basename "$entry")"
    rm -rf "$DST/$name"
    cp -R "$entry" "$DST/$name"
    echo "synced $name"
    found_any=true
done

if [ "$found_any" = false ]; then
    echo "Error: source dir is empty: $SRC" >&2
    exit 1
fi

# 反向清理：目标里有、源里没有的条目删掉
for entry in "$DST"/*; do
    name="$(basename "$entry")"
    if [ ! -e "$SRC/$name" ]; then
        rm -rf "$DST/$name"
        echo "removed stale $name"
    fi
done

echo "Frontend synced: cmd/server/web -> cmd/mobile/web"
