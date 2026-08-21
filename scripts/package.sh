#!/usr/bin/env bash
#
# 打包脚本：产出各平台可执行文件，以及 macOS 的 .app 应用包。
#
#   ./scripts/package.sh            全平台
#   ./scripts/package.sh mac        只打 macOS
#
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
LDFLAGS="-s -w -X main.Version=${VERSION}"
DIST="dist"
APP_NAME="frp-ngrok"
TARGET="${1:-all}"

rm -rf "$DIST"
mkdir -p "$DIST"

echo "==> 版本 ${VERSION}"

# 非 macOS 平台不含菜单栏，关闭 CGO 以便纯静态交叉编译
build() {
    local goos="$1" goarch="$2" out="$3"
    echo "    ${goos}/${goarch} -> ${out}"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
        go build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$out" .
}

# 菜单栏调用了系统原生界面接口，macOS 必须开 CGO 并指定目标架构
build_mac() {
    local goarch="$1" clang_arch="$2" out="$3"
    echo "    darwin/${goarch} -> ${out}"
    GOOS=darwin GOARCH="$goarch" CGO_ENABLED=1 CC="clang -arch ${clang_arch}" \
        go build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$out" .
}

# ---------- macOS ----------
if [ "$TARGET" = "all" ] || [ "$TARGET" = "mac" ]; then
    echo "==> 生成图标"
    go run ./cmd/genicon

    echo "==> 构建 macOS"
    build_mac amd64 x86_64 frp-ngrok-darwin-amd64
    build_mac arm64 arm64 frp-ngrok-darwin-arm64

    # 合成通用二进制，Intel 与 Apple Silicon 同一个包通用
    UNIVERSAL="$DIST/frp-ngrok-darwin-universal"
    if command -v lipo >/dev/null 2>&1; then
        lipo -create -output "$UNIVERSAL" \
            "$DIST/frp-ngrok-darwin-amd64" "$DIST/frp-ngrok-darwin-arm64"
    else
        cp "$DIST/frp-ngrok-darwin-amd64" "$UNIVERSAL"
    fi

    echo "==> 生成 ${APP_NAME}.app"
    APP="$DIST/${APP_NAME}.app"
    mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
    cp "$UNIVERSAL" "$APP/Contents/MacOS/frp-ngrok"
    chmod +x "$APP/Contents/MacOS/frp-ngrok"
    cp build/AppIcon.icns "$APP/Contents/Resources/AppIcon.icns"

    cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleExecutable</key>
    <string>frp-ngrok</string>
    <key>CFBundleIconFile</key>
    <string>AppIcon</string>
    <key>CFBundleIdentifier</key>
    <!-- v2 只迁移一次应用身份：旧 v1 启动器仍占着菜单栏时，Finder 也会真正
         启动这个新包。v2 启动后会把菜单栏交给独立进程，自身不再长期驻留。 -->
    <string>com.frpngrok.launcher.v2</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>NSHumanReadableCopyright</key>
    <string>Copyright 2026 The frp-ngrok Authors. Apache License 2.0.</string>
    <key>LSMinimumSystemVersion</key>
    <string>11.0</string>
    <!-- 启动器打开浏览器后即退出，不需要占用程序坞 -->
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
PLIST

    # 未签名的应用从网上下载后会被 Gatekeeper 拦，打包时先清掉隔离属性
    xattr -cr "$APP" 2>/dev/null || true

    (cd "$DIST" && zip -qr "${APP_NAME}-macOS-${VERSION}.zip" "${APP_NAME}.app")
    echo "    $DIST/${APP_NAME}-macOS-${VERSION}.zip"
fi

# ---------- 其他平台 ----------
if [ "$TARGET" = "all" ]; then
    echo "==> 构建 Windows / Linux"
    build windows amd64 frp-ngrok-windows-amd64.exe
    build windows arm64 frp-ngrok-windows-arm64.exe
    build linux amd64 frp-ngrok-linux-amd64
    build linux arm64 frp-ngrok-linux-arm64
fi

echo
echo "==> 完成，产物在 $DIST/"
ls -lh "$DIST" | tail -n +2
