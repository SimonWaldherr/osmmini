#!/usr/bin/env bash
# Build from any working directory. Requires macOS, full Xcode and Go.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." > /dev/null && pwd -P)"
mode="${1:-all}"
if [[ $# -gt 1 ]]; then echo "Usage: $0 [all|macos]" >&2; exit 2; fi
case "$mode" in all|macos) ;; *) echo "Usage: $0 [all|macos]" >&2; exit 2 ;; esac
if [[ "$(uname -s)" != Darwin ]]; then
    echo "XCFramework builds require macOS and Xcode." >&2
    exit 1
fi
command -v go >/dev/null
command -v xcodebuild >/dev/null
build="$root/local/build/apple"
package="$root/apple"
mkdir -p "$build"
# Use a unique staging directory; retain the last complete framework on failure.
stage="$(mktemp -d "$build/staging.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
cat > "$stage/clang" <<'CLANG'
#!/usr/bin/env bash
exec "$OSMMINI_APPLE_CC" "$@"
CLANG
chmod +x "$stage/clang"
cd "$root"

build_slice() {
    local name="$1" sdk="$2" os="$3" arch="$4" target="$5"
    local sdk_path compiler
    sdk_path="$(xcrun --sdk "$sdk" --show-sdk-path)"
    compiler="$(xcrun --sdk "$sdk" --find clang)"
    mkdir -p "$stage/$name"
    echo "Building $name ($target)"
    OSMMINI_APPLE_CC="$compiler" GOOS="$os" GOARCH="$arch" CGO_ENABLED=1 \
        CC="\"$stage/clang\"" \
        CGO_CFLAGS="-target $target -isysroot \"$sdk_path\"" \
        CGO_LDFLAGS="-target $target -isysroot \"$sdk_path\"" \
        go build -trimpath -buildmode=c-archive \
        -o "$stage/$name/libCOSMmini.a" ./bindings/c
}

build_slice macos-arm64 macosx darwin arm64 arm64-apple-macos14.0
build_slice macos-amd64 macosx darwin amd64 x86_64-apple-macos14.0
mkdir -p "$stage/macos"
xcrun lipo -create "$stage/macos-arm64/libCOSMmini.a" "$stage/macos-amd64/libCOSMmini.a" \
    -output "$stage/macos/libCOSMmini.a"
args=(-library "$stage/macos/libCOSMmini.a" -headers "$root/bindings/c/include")
if [[ "$mode" == all ]]; then
    build_slice ios-arm64 iphoneos ios arm64 arm64-apple-ios17.0
    build_slice simulator-arm64 iphonesimulator ios arm64 arm64-apple-ios17.0-simulator
    build_slice simulator-amd64 iphonesimulator ios amd64 x86_64-apple-ios17.0-simulator
    mkdir -p "$stage/simulator"
    xcrun lipo -create "$stage/simulator-arm64/libCOSMmini.a" "$stage/simulator-amd64/libCOSMmini.a" \
        -output "$stage/simulator/libCOSMmini.a"
    args+=(-library "$stage/ios-arm64/libCOSMmini.a" -headers "$root/bindings/c/include")
    args+=(-library "$stage/simulator/libCOSMmini.a" -headers "$root/bindings/c/include")
fi
xcodebuild -create-xcframework "${args[@]}" -output "$stage/COSMmini.xcframework"
rm -rf "$package/COSMmini.xcframework"
mv "$stage/COSMmini.xcframework" "$package/COSMmini.xcframework"
echo "Ready: $package (add this local package in Xcode)"
