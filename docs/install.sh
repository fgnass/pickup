#!/bin/sh
# Installs Pickup into ~/Applications.
#   curl -fsSL https://pickup.gnass.buzz/install.sh | sh
#
# A download with curl gets no quarantine flag, so macOS opens the app without
# the "unidentified developer" warning.
set -eu

URL="${PICKUP_URL:-https://github.com/fgnass/pickup/releases/latest/download/Pickup.zip}"
DEST="$HOME/Applications"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading Pickup…"
curl -fsSL "$URL" -o "$TMP/Pickup.zip"

pkill -x Pickup 2>/dev/null || true
mkdir -p "$DEST"
rm -rf "$DEST/Pickup.app"
ditto -x -k "$TMP/Pickup.zip" "$DEST"

# Make the Finder service "Share via QR Code" show up right away.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$DEST/Pickup.app"
/System/Library/CoreServices/pbs -update

echo "Installed to $DEST/Pickup.app"
echo "Right-click a file in the Finder and choose Share via QR Code."
open "$DEST/Pickup.app"
