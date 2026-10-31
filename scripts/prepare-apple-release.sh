#!/usr/bin/env bash
# Stage an immutable XCFramework asset and the matching remote SwiftPM manifest.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." >/dev/null && pwd -P)"
if [[ $# != 2 ]]; then echo "Usage: $0 VERSION HTTPS_RELEASE_BASE_URL" >&2; exit 2; fi
version="$1"; base="${2%/}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][a-zA-Z0-9.-]+)?$ ]] || { echo "Invalid version" >&2; exit 2; }
[[ "$base" == https://* ]] || { echo "Use an HTTPS release URL" >&2; exit 2; }
test -d "$root/apple/COSMmini.xcframework" || { echo "Run make build-apple first" >&2; exit 1; }
output="$root/local/build/apple/release-$version"
mkdir -p "$output"
asset="COSMmini-$version.xcframework.zip"
ditto -c -k --keepParent "$root/apple/COSMmini.xcframework" "$output/$asset"
checksum="$(swift package compute-checksum "$output/$asset")"
python3 - "$root" "$output" "$base/$asset" "$checksum" <<'PY'
from pathlib import Path
import json,sys
root,out,url,checksum=sys.argv[1:]
text=Path(root,'Package.swift').read_text()
text=text.replace('.binaryTarget(name: "COSMmini", path: "apple/COSMmini.xcframework")',
    '.binaryTarget(name: "COSMmini", url: '+json.dumps(url)+', checksum: '+json.dumps(checksum)+')')
Path(out,'Package.swift').write_text(text)
Path(out,'checksum.txt').write_text(checksum+'\n')
PY
echo "Staged framework, checksum and release Package.swift in $output"
