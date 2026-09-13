#!/usr/bin/env bash
set -euo pipefail

# build.sh - builds xray-cli for macOS only.
#
# Usage:
#   ./build.sh                       # macOS, current arch
#   ./build.sh --arch arm64          # macOS, arm64
#   ./build.sh --arch amd64          # macOS, amd64
#
# Output:
#   ./dist/darwin/xray-cli-<arch>

PKG_DIR="./src"   # Go source lives in ./src, not the repo root
OUT_DIR="$(pwd)/dist"

usage() {
  cat <<EOF
Usage: $0 [--arch amd64|arm64]

  (no flags)         Build for macOS and the current arch.
  --arch <arch>      Target macOS arch: amd64 or arm64. Default: current arch.

Output: ./dist/darwin/xray-cli-<arch>

Notes:
  - Builds use your local Go toolchain with CGO enabled, so --tray works.
  - Cross-arch macOS builds are supported (e.g.
    macOS arm64 host building macOS amd64) via Xcode's cross-arch clang.
EOF
}

target_os=""
target_arch=""

while [ $# -gt 0 ]; do
  case "$1" in
    --os)
      target_os="${2:-}"
      if [ -z "$target_os" ]; then
        echo "Error: --os requires a value." >&2
        usage
        exit 1
      fi
      shift 2
      ;;
    --arch)
      target_arch="${2:-}"
      if [ -z "$target_arch" ]; then
        echo "Error: --arch requires a value (amd64 or arm64)." >&2
        usage
        exit 1
      fi
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Error: unknown argument '$1'." >&2
      usage
      exit 1
      ;;
  esac
done

if [ ! -d "$PKG_DIR" ]; then
  echo "Error: ${PKG_DIR} not found relative to $(pwd). Run this script from the repo root that contains the 'src' directory." >&2
  exit 1
fi

if [ -n "$target_arch" ]; then
  case "$target_arch" in
    amd64|arm64) ;;
    *)
      echo "Error: --arch must be 'amd64' or 'arm64', got '${target_arch}'." >&2
      exit 1
      ;;
  esac
fi

if [ -n "$target_os" ]; then
  echo "Error: --os is no longer supported. This project builds macOS binaries only." >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Error: 'go' not found on PATH. Install Go 1.26+." >&2
  exit 1
fi

local_os="$(go env GOOS)"
local_arch="$(go env GOARCH)"

if [ "$local_os" != "darwin" ]; then
  echo "Error: this project only supports macOS builds; current GOOS is '${local_os}'." >&2
  exit 1
fi

if [ -z "$target_arch" ]; then
  target_arch="$local_arch"
fi

build_os="darwin"
out_subdir="${OUT_DIR}/${build_os}"
out_name="xray-cli-${target_arch}"
mkdir -p "$out_subdir"

if [ "$target_arch" != "$local_arch" ]; then
  echo "Cross-compiling ${build_os}/${target_arch} from ${build_os}/${local_arch}."
  echo "Note: cgo-dependent code (systray/--tray) may not build or may misbehave"
  echo "under cross-arch compilation without a matching cgo cross-toolchain for"
  echo "${target_arch} installed on this machine. If the build below fails on"
  echo "systray symbols, you likely need that toolchain, or build natively on"
  echo "a machine of that arch instead."
  echo
fi

echo "Building ${build_os}/${target_arch} (CGO_ENABLED=1)..."
GOARCH="$target_arch" CGO_ENABLED=1 \
  go build -ldflags="-s -w" -o "${out_subdir}/${out_name}" "$PKG_DIR"

echo "  -> dist/${build_os}/${out_name}"
echo

echo "Built for macOS - --tray is supported in this binary."

echo
echo "Done. Binary:"
ls -la "${out_subdir}/${out_name}"
