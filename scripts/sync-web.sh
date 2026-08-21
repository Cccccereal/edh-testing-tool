#!/bin/bash
# 把桌面端 cmd/server/web 的前端资源同步到移动端 cmd/mobile/web。
# 两处前端文件（index.html / styles.css / app.js）必须保持一致：
# 移动端通过 gomobile 把 cmd/mobile 打成 AAR 内嵌 Web 资源，改动只在桌面端落地的话，
# 移动端 APK 会继续使用旧版前端。改动任何前端后请运行本脚本再提交。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
SRC="$PROJECT_ROOT/cmd/server/web"
DST="$PROJECT_ROOT/cmd/mobile/web"

FILES=("index.html" "styles.css" "app.js")

for file in "${FILES[@]}"; do
    from="$SRC/$file"
    to="$DST/$file"
    if [ ! -f "$from" ]; then
        echo "Error: Missing source file: $from" >&2
        exit 1
    fi
    cp "$from" "$to"
    echo "synced $file"
done

echo "Frontend synced: cmd/server/web -> cmd/mobile/web"
