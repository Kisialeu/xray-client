#!/usr/bin/env bash
set -euo pipefail

# build.sh — builds xray-cli in one of two modes:
#
#   1) LOCAL (default): builds with your own Go toolchain, for the current
#      OS, optionally cross-arch via --arch. CGO enabled — on macOS this
#      means --tray works. Cross-OS is NOT done this way: github.com/
#      getlantern/systray needs cgo/Cocoa, and cross-OS cgo from macOS
#      needs a C cross-toolchain this script doesn't set up.
#
#   2) DOCKER, Linux only (--os linux): runs `go build` inside a pinned
#      golang Docker image targeting linux/amd64 or linux/arm64, with
#      CGO_ENABLED=0. This is safe specifically for Linux because the
#      systray/cgo code is excluded by its own `GOOS=darwin`-only build
#      constraint regardless of CGO_ENABLED — unlike darwin, where
#      CGO_ENABLED=0 makes the build fail outright on undefined systray
#      symbols. --tray is unavailable at runtime on Linux either way
#      (tray_other.go panics by design), so this is not a regression.
#
# Usage:
#   ./build.sh                       # native: current OS + arch
#   ./build.sh --arch arm64          # current OS, arm64
#   ./build.sh --arch amd64          # current OS, amd64
#   ./build.sh --os linux            # Docker: linux, current Go GOARCH default (amd64)
#   ./build.sh --os linux --arch arm64
#
# Output:
#   ./dist/<os>/xray-cli-<arch>

PKG_DIR="./src"   # Go source lives in ./src, not the repo root
OUT_DIR="$(pwd)/dist"
SRC_ROOT="$(pwd)"
GO_IMAGE="golang:1.25"

usage() {
  cat <<EOF
Usage: $0 [--os <os>] [--arch amd64|arm64]

  (no flags)         Build natively for the current OS and arch.
  --arch <arch>      Target arch: amd64 or arm64. Default: current arch.
  --os <os>          Target OS. Only 'linux' is supported as a non-native
                      value, and it routes through Docker (no local Linux
                      toolchain needed). Any other value, or omitting this
                      flag, builds for the current (native) OS using your
                      local Go toolchain.

Output: ./dist/<os>/xray-cli-<arch>

Notes:
  - Native-OS builds use your local Go toolchain with CGO enabled, so
    --tray works on macOS. Cross-arch on the same OS is supported (e.g.
    macOS arm64 host building macOS amd64) via Xcode's cross-arch clang.
  - --os linux always builds with CGO_ENABLED=0 inside Docker. --tray is
    unavailable at runtime on Linux regardless (it panics by design), so
    this has no effect on tray functionality.
  - There is no supported way to cross-build a darwin binary from Linux,
    or vice versa, through this script — see header comment for why.
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

# ── Docker / Linux path ──────────────────────────────────────────────────
if [ "$target_os" = "linux" ]; then
  if ! command -v docker >/dev/null 2>&1; then
    echo "Error: 'docker' not found on PATH. Required for --os linux." >&2
    exit 1
  fi

  arch="${target_arch:-amd64}"
  out_subdir="${OUT_DIR}/linux"
  out_name="xray-cli-${arch}"
  mkdir -p "$out_subdir"

  echo "Building linux/${arch} via Docker (${GO_IMAGE}, CGO_ENABLED=0)..."
  echo "Note: --tray is unavailable at runtime on Linux (panics by design);"
  echo "this build excludes the cgo/systray path naturally via GOOS=linux,"
  echo "it does not need cgo at all for this target."
  echo

  docker pull "$GO_IMAGE" >/dev/null

  docker run --rm \
    -e GOOS=linux \
    -e GOARCH="$arch" \
    -e CGO_ENABLED=0 \
    -e GOCACHE=/tmp/gocache \
    -v "${SRC_ROOT}:/repo:ro" \
    -v "${out_subdir}:/out" \
    -w /repo \
    "$GO_IMAGE" \
    go build -ldflags="-s -w" -o "/out/${out_name}" ./src

  echo "  -> dist/linux/${out_name}"
  echo
  echo "Note: this is a Linux binary — running it directly on macOS will fail"
  echo "with 'exec format error'. Copy it to a Linux host or container to run."
  echo
  echo "Done. Binary:"
  ls -la "${out_subdir}/${out_name}"
  exit 0
fi

if [ -n "$target_os" ]; then
  echo "Error: --os '${target_os}' is not supported. Only 'linux' is handled" >&2
  echo "as a non-native target (via Docker). Omit --os to build natively for" >&2
  echo "your current OS, or run this script on a machine of the OS you need." >&2
  exit 1
fi

# ── Local / native-OS path (with optional cross-arch) ────────────────────
if ! command -v go >/dev/null 2>&1; then
  echo "Error: 'go' not found on PATH. Install Go 1.25+, or use '--os linux' for a Docker-based Linux build instead." >&2
  exit 1
fi

local_os="$(go env GOOS)"
local_arch="$(go env GOARCH)"

if [ -z "$target_arch" ]; then
  target_arch="$local_arch"
fi

build_os="$local_os"
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

if [ "$build_os" = "darwin" ]; then
  echo "Built on macOS — --tray is supported in this binary."
else
  echo "Built on ${build_os} — --tray is macOS-only and will panic if passed on this binary."
fi

echo
echo "Done. Binary:"
ls -la "${out_subdir}/${out_name}"