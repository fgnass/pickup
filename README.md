# Pickup

Pack files and folders on the Mac, and let a phone pick them up. Right-click in
the Finder, choose **Share via QR Code**, and scan the shipping
label that opens. The phone shows the parcel: download single files, browse
folders, or get it all as ZIP.

Everything stays on the local network. The phone needs no app and no internet.

**Website:** https://pickup.gnass.buzz

## Install

```sh
curl -fsSL https://pickup.gnass.buzz/install.sh | sh
```

Or download `Pickup.zip` from the [latest release](https://github.com/fgnass/pickup/releases/latest).
Requires macOS 14 or later (Apple silicon or Intel).

## How it works

- `app/` — Swift menu-less app. It registers the Finder service (`NSServices`),
  shows the QR window and runs the helper. It quits when the last window closes.
- `helper/` — Go binary with [Pion](https://github.com/pion/webrtc). It serves
  the phone page over plain HTTP and sends the files over a WebRTC data channel.

Security:

- The QR URL is `http://<lan-ip>:<port>/#<secret><fingerprint>`. The fragment
  never leaves the phone's browser.
- The phone checks that the Mac's DTLS fingerprint matches the one from the QR
  code, then sends the secret inside the encrypted data channel. Without the
  secret, the Mac closes the connection after 15 seconds and sends nothing.
- Folder access goes through `os.Root`, so no path or symlink can leave the
  shared folder. Hidden files are not shared.
- Limit: the page's JavaScript comes over plain HTTP. An active attacker on the
  same network could change it. A passive listener sees only encrypted data.

## Build

```sh
./build.sh           # build/Pickup.app
./build.sh install   # also copy to ~/Applications and register the service
./build.sh release   # also write build/Pickup.zip (universal) for a GitHub release
(cd helper && go test .)        # ZIP and path tests
swift scripts/make-icon.swift  # regenerate app/AppIcon.icns; see the script for the PNGs
```

Requires Go and the Xcode Command Line Tools.

The label shows the Mac's name as sender. To use another name:
`defaults write buzz.gnass.pickup MacName "Studio Mac"`.

`docs/` is the project page (GitHub Pages, served from `/docs`). It also hosts
`install.sh`, which downloads the latest release ZIP with `curl`. Files loaded
this way get no quarantine flag, so the ad-hoc signed app opens without a
Gatekeeper warning.

The photos in the screenshots on the project page are from [Unsplash](https://unsplash.com/s/photos/lisbon) (Unsplash License).

## License

MIT, see [LICENSE](LICENSE). The Roboto Flex font is licensed under the SIL Open
Font License, see [licenses/RobotoFlex-OFL.txt](licenses/RobotoFlex-OFL.txt).
