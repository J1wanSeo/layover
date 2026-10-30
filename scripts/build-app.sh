#!/bin/sh
# Builds build/Layover.app (universal: Apple Silicon + Intel) and
# build/Layover-<version>.zip for GitHub Releases.
# The version comes from packaging/Info.plist (CFBundleShortVersionString).
set -eu
cd "$(dirname "$0")/.."

APP=build/Layover.app
VERSION=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" packaging/Info.plist)
# Command Line Tools can't initialize the default (swift-build) system; native works.
SWIFT="swift build -c release --package-path app --build-system native"

mkdir -p build
for arch in arm64 amd64; do
	GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
		-o "build/myvpn-$arch" ./cmd/myvpn
done
lipo -create -output build/myvpn build/myvpn-arm64 build/myvpn-amd64
rm build/myvpn-arm64 build/myvpn-amd64

for triple in arm64-apple-macosx13.0 x86_64-apple-macosx13.0; do
	$SWIFT --triple "$triple"
done
lipo -create -output build/Layover \
	"$($SWIFT --triple arm64-apple-macosx13.0 --show-bin-path)/Layover" \
	"$($SWIFT --triple x86_64-apple-macosx13.0 --show-bin-path)/Layover"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp packaging/Info.plist "$APP/Contents/"
cp packaging/AppIcon.icns "$APP/Contents/Resources/"
cp LICENSE THIRD_PARTY_LICENSES.txt "$APP/Contents/Resources/"
mv build/Layover "$APP/Contents/MacOS/Layover"
mv build/myvpn "$APP/Contents/MacOS/myvpn"

# Ad-hoc signature: runs without an Apple Developer account, but downloaded
# copies need "Open Anyway" in System Settings (see README).
codesign --force --sign - "$APP/Contents/MacOS/myvpn"
codesign --force --sign - "$APP"

ZIP="build/Layover-$VERSION.zip"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP" "$ZIP"

echo "built $APP ($VERSION, $(lipo -archs "$APP/Contents/MacOS/Layover"))"
echo "built $ZIP"
