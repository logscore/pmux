#!/usr/bin/env bash
set -euo pipefail

REPO="logscore/roxy"
INSTALL_DIR="/usr/local/bin"
BINARY="roxy"

# --- helpers ----------------------------------------------------------------

die() { echo "error: $*" >&2; exit 1; }

detect_platform() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"

  case "$os" in
    Darwin) os="darwin" ;;
    Linux)  os="linux"  ;;
    *)      die "unsupported OS: $os (only macOS and Linux are supported)" ;;
  esac

  case "$arch" in
    x86_64|amd64)  arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *)             die "unsupported architecture: $arch" ;;
  esac

  echo "${os}/${arch}"
}

latest_version() {
  local url="https://api.github.com/repos/${REPO}/releases/latest"
  if command -v curl &>/dev/null; then
    curl -fsSL "$url" | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/'
  elif command -v wget &>/dev/null; then
    wget -qO- "$url" | grep '"tag_name"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/'
  else
    die "curl or wget is required"
  fi
}

download() {
  local url="$1" dest="$2"
  if command -v curl &>/dev/null; then
    curl -fsSL -o "$dest" "$url"
  elif command -v wget &>/dev/null; then
    wget -qO "$dest" "$url"
  fi
}

verify_checksum() {
  local binary_path="$1" checksums_url="$2" expected_name="$3"
  local tmpfile
  tmpfile="$(mktemp)"
  download "$checksums_url" "$tmpfile"

  local expected
  expected="$(grep "$expected_name" "$tmpfile" | awk '{print $1}')"
  rm -f "$tmpfile"

  if [ -z "$expected" ]; then
    echo "warning: could not find checksum for ${expected_name}, skipping verification" >&2
    return 0
  fi

  local actual
  if command -v sha256sum &>/dev/null; then
    actual="$(sha256sum "$binary_path" | awk '{print $1}')"
  elif command -v shasum &>/dev/null; then
    actual="$(shasum -a 256 "$binary_path" | awk '{print $1}')"
  else
    echo "warning: no sha256 tool found, skipping checksum verification" >&2
    return 0
  fi

  if [ "$actual" != "$expected" ]; then
    die "checksum mismatch (expected ${expected}, got ${actual})"
  fi
}

# --- main -------------------------------------------------------------------

main() {
  local version="${1:-}"
  local platform
  platform="$(detect_platform)"
  local os="${platform%/*}"
  local arch="${platform#*/}"

  echo "Detected platform: ${os}/${arch}"

  if [ -z "$version" ]; then
    echo "Fetching latest release..."
    version="$(latest_version)"
    if [ -z "$version" ]; then
      die "could not determine latest version. Pass a version explicitly: ./install.sh v1.0.0"
    fi
  fi

  local asset="${BINARY}-${os}-${arch}"
  local download_url="https://github.com/${REPO}/releases/download/${version}/${asset}"
  local checksums_url="https://github.com/${REPO}/releases/download/${version}/checksums.txt"

  echo "Installing roxy ${version} (${os}/${arch})..."

  local tmpdir
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  echo "Downloading ${download_url}..."
  download "$download_url" "${tmpdir}/${asset}"

  echo "Verifying checksum..."
  verify_checksum "${tmpdir}/${asset}" "$checksums_url" "$asset"

  chmod +x "${tmpdir}/${asset}"

  if [ -w "$INSTALL_DIR" ]; then
    mv "${tmpdir}/${asset}" "${INSTALL_DIR}/${BINARY}"
  else
    echo "Installing to ${INSTALL_DIR} (requires sudo)..."
    sudo mv "${tmpdir}/${asset}" "${INSTALL_DIR}/${BINARY}"
  fi

  echo "roxy ${version} installed to ${INSTALL_DIR}/${BINARY}"
}

main "$@"
