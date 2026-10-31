#!/usr/bin/env bash
# Produce a self-contained Swift package ZIP for local SwiftPM/Xcode import.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." >/dev/null && pwd -P)"
output="${1:-$root/local/build/apple/OSMmini-Apple.zip}"
test -d "$root/apple/COSMmini.xcframework" || { echo "Run make build-apple first." >&2; exit 1; }
mkdir -p "$(dirname "$output")"
output="$(cd "$(dirname "$output")" >/dev/null && pwd -P)/$(basename "$output")"
mkdir -p "$root/local/build/apple"
stage="$(mktemp -d "$root/local/build/apple/package.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/OSMmini"
cp -R "$root/apple/Sources" "$root/apple/Tests" "$root/apple/COSMmini.xcframework" "$stage/OSMmini/"
# Copy the shared demo project without local Xcode user state or workspace data.
mkdir -p "$stage/OSMmini/Demo/OSMminiDemo.xcodeproj"
cp "$root/apple/Demo/"*.swift "$root/apple/Demo/Info.plist" "$stage/OSMmini/Demo/"
cp -R "$root/apple/Demo/Assets.xcassets" "$stage/OSMmini/Demo/"
# Select shared signing configuration explicitly; never package personal settings.
mkdir -p "$stage/OSMmini/Demo/Configuration"
cp "$root/apple/Demo/Configuration/Signing.xcconfig" \
   "$root/apple/Demo/Configuration/Signing.local.xcconfig.example" \
   "$stage/OSMmini/Demo/Configuration/"
cp "$root/apple/Demo/OSMminiDemo.xcodeproj/project.pbxproj" "$stage/OSMmini/Demo/OSMminiDemo.xcodeproj/"
cp -R "$root/apple/Demo/OSMminiDemo.xcodeproj/xcshareddata" "$stage/OSMmini/Demo/OSMminiDemo.xcodeproj/"
cp "$root/apple/Package.swift" "$root/apple/README.md" "$root/apple/NOTICE" "$root/apple/LICENSE" "$stage/OSMmini/"
(cd "$stage" >/dev/null && ditto -c -k --keepParent OSMmini "$output")
echo "$output"
