#!/bin/bash
# Builds UnityDoorstop for macOS 11 and later (Intel and Apple silicon) into
# internal/mods/dist, where the Mac app picks it up. Doorstop loads BepInEx,
# and so the mods, into the macOS server; the one in BepInExPack_Valheim is
# built for the newest macOS only. UnityDoorstop is LGPL-2.1: its license
# goes with the app, and the source is the exact commit below.
set -euo pipefail
commit=1c3ece1f1e6c20f4aa74d5402baabf8a0098d282 # v4.6.0, the version BepInExPack_Valheim uses
root="$(cd "$(dirname "$0")/../.." && pwd)"
src="$RUNNER_TEMP/doorstop-src"
rm -rf "$src"
git init --quiet "$src"
git -C "$src" fetch --quiet --depth 1 https://github.com/NeighTools/UnityDoorstop "$commit"
git -C "$src" checkout --quiet FETCH_HEAD
test "$(git -C "$src" rev-parse HEAD)" = "$commit"
cd "$src"
clang -dynamiclib -Os -Isrc -arch x86_64 -arch arm64 -mmacosx-version-min=11.0 \
  -o "$root/internal/mods/dist/libdoorstop.dylib" \
  src/*.c src/config/*.c src/util/*.c src/runtimes/*.c src/nix/*.c src/nix/plthook/plthook_osx.c -ldl
cp LICENSE "$root/internal/mods/dist/UnityDoorstop-LICENSE.txt"
lipo -info "$root/internal/mods/dist/libdoorstop.dylib"
otool -l "$root/internal/mods/dist/libdoorstop.dylib" | grep -A4 LC_BUILD_VERSION || true
