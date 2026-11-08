#!/bin/sh
# Builds Pickup.app (universal: Apple silicon and Intel) into build/.
#   ./build.sh install   also copy it to ~/Applications and register the service
#   ./build.sh release   also write build/Pickup.zip for a GitHub release
set -eu
cd "$(dirname "$0")"

APP=build/Pickup.app
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

TMP=build/tmp
mkdir -p "$TMP"
for ARCH in arm64 x86_64; do
  GOARCH=$ARCH; [ "$ARCH" = x86_64 ] && GOARCH=amd64
  echo "→ helper (Go, $ARCH)"
  (cd helper && CGO_ENABLED=0 GOOS=darwin GOARCH=$GOARCH go build -trimpath -ldflags="-s -w" -o "../$TMP/helper-$ARCH" .)
  echo "→ app (Swift, $ARCH)"
  swiftc -O -swift-version 6 -target $ARCH-apple-macos14 -o "$TMP/app-$ARCH" app/main.swift
done
lipo -create "$TMP/helper-arm64" "$TMP/helper-x86_64" -output "$APP/Contents/MacOS/pickup-helper"
lipo -create "$TMP/app-arm64" "$TMP/app-x86_64" -output "$APP/Contents/MacOS/Pickup"
rm -rf "$TMP"

cp app/Info.plist "$APP/Contents/"
cp app/AppIcon.icns "$APP/Contents/Resources/"

echo "→ sign (ad hoc)"
codesign --force --sign - "$APP/Contents/MacOS/pickup-helper"
codesign --force --sign - "$APP"

if [ "${1:-}" = "install" ]; then
  DEST="$HOME/Applications/Pickup.app"
  echo "→ install to $DEST"
  pkill -x Pickup 2>/dev/null || true
  mkdir -p "$HOME/Applications"
  rm -rf "$DEST"
  cp -R "$APP" "$DEST"
  /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$DEST"
  /System/Library/CoreServices/pbs -update
fi

if [ "${1:-}" = "release" ]; then
  echo "→ build/Pickup.zip"
  rm -f build/Pickup.zip
  ditto -c -k --keepParent "$APP" build/Pickup.zip
fi
echo "done"
