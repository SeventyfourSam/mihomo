#!/bin/sh
set -eu

# Build and sign the standalone macOS arm64 executable; no .app bundle is made.
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"

if [ "$(uname -s)" != Darwin ]; then
    printf '%s\n' 'This build requires macOS for codesign.' >&2
    exit 1
fi

custom_version=${VERSION:-v1.19.31-custom}
binary=bin/miumiu-darwin-arm64
archive=$binary-$custom_version.gz

make darwin-arm64 NAME=miumiu VERSION="$custom_version" BINDIR=bin
codesign --force --sign - --identifier miumiu "$binary"
codesign --verify --strict --verbose=2 "$binary"

# Keep the executable as well as the distributable archive.
gzip -n -9 -c "$binary" > "$archive"
printf 'Executable: %s\nArchive: %s\n' "$binary" "$archive"
