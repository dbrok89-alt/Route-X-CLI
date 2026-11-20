#!/bin/sh
set -e

REPO="dbrok89-alt/Route-X-CLI"
BINARY_NAME="route-x"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  linux|darwin) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

ASSET_NAME="${BINARY_NAME}-${OS}-${ARCH}"

DEST_DIR="/usr/local/bin"
if [ ! -w "$DEST_DIR" ]; then
  DEST_DIR="$HOME/.local/bin"
  mkdir -p "$DEST_DIR"
fi

echo "Installing ${BINARY_NAME}..."

DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${ASSET_NAME}"

if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$DOWNLOAD_URL" -o "${DEST_DIR}/${BINARY_NAME}" 2>/dev/null || true
elif command -v wget >/dev/null 2>&1; then
  wget -qO "${DEST_DIR}/${BINARY_NAME}" "$DOWNLOAD_URL" 2>/dev/null || true
fi

# A failed download (e.g. an HTML 404 page) must never be installed
# as the binary: verify the magic bytes first — ELF on Linux,
# Mach-O on macOS (a darwin binary is never ELF, so checking only
# for ELF would delete every valid macOS download).
if [ -s "${DEST_DIR}/${BINARY_NAME}" ]; then
  MAGIC=$(head -c 4 "${DEST_DIR}/${BINARY_NAME}" | od -An -tx1 | tr -d ' \n')
  VALID=0
  case "$MAGIC" in
    7f454c46) [ "$OS" = "linux" ] && VALID=1 ;;
    cffaedfe|feedfacf|cefaedfe|feedface|cafebabe) [ "$OS" = "darwin" ] && VALID=1 ;;
  esac
  if [ "$VALID" != "1" ]; then
    echo "Downloaded file is not a valid binary (magic: $MAGIC). Falling back to building from source..."
    rm -f "${DEST_DIR}/${BINARY_NAME}"
  fi
fi

if [ ! -s "${DEST_DIR}/${BINARY_NAME}" ]; then
  echo "Release binary download failed. Falling back to building from source..."
  # NOTE: `go install github.com/${REPO}@latest` does NOT work here:
  # go.mod declares `module route-x`, so Go refuses it with
  # "module declares its path as: route-x but was required as:
  # github.com/dbrok89-alt/Route-X-CLI" (verified with go1.23.0).
  # Build from the source tarball instead — same result, no
  # module path involved, and no `git` needed (only curl/wget,
  # tar and go, which the script already assumes). Prefer the
  # latest release tag (what `go install @latest` would have
  # built); fall back to main if the tag cannot be resolved.
  if command -v go >/dev/null 2>&1; then
    BUILD_DIR="${TMPDIR:-/tmp}/route-x-build-$$"
    rm -rf "$BUILD_DIR"
    mkdir -p "$BUILD_DIR"
    TAG=""
    if command -v curl >/dev/null 2>&1; then
      LATEST_URL=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" 2>/dev/null || true)
      case "$LATEST_URL" in */releases/tag/*) TAG=${LATEST_URL##*/};; esac
    fi
    if [ -n "$TAG" ]; then
      ARCHIVE_URL="https://github.com/${REPO}/archive/refs/tags/${TAG}.tar.gz"
    else
      ARCHIVE_URL="https://github.com/${REPO}/archive/refs/heads/main.tar.gz"
    fi
    TARBALL="$BUILD_DIR/source.tar.gz"
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL "$ARCHIVE_URL" -o "$TARBALL" 2>/dev/null || true
    elif command -v wget >/dev/null 2>&1; then
      wget -qO "$TARBALL" "$ARCHIVE_URL" 2>/dev/null || true
    fi
    # The tarball's top directory is ${REPO##*/}-<ref>, but GitHub
    # strips a leading `v` from tags (v0.5.0 extracts to
    # Route-X-CLI-0.5.0), so never guess the name — take whatever
    # directory the extraction produced.
    SRC_DIR=""
    if [ -s "$TARBALL" ] && tar -xzf "$TARBALL" -C "$BUILD_DIR" 2>/dev/null; then
      for d in "$BUILD_DIR"/${REPO##*/}-*; do
        if [ -d "$d" ]; then SRC_DIR="$d"; break; fi
      done
    fi
    if [ -n "$SRC_DIR" ] && (cd "$SRC_DIR" && go build -o "${DEST_DIR}/${BINARY_NAME}" .); then
      rm -rf "$BUILD_DIR"
      chmod +x "${DEST_DIR}/${BINARY_NAME}"
      echo "${BINARY_NAME} installed from source successfully to ${DEST_DIR}/${BINARY_NAME}"
      exit 0
    fi
    rm -rf "$BUILD_DIR"
    echo "Error: Could not download the release binary and building from source failed."
    exit 1
  else
    echo "Error: Could not download the release binary and 'go' (needed to build from source) is not installed."
    exit 1
  fi
fi

chmod +x "${DEST_DIR}/${BINARY_NAME}"
echo "${BINARY_NAME} installed successfully to ${DEST_DIR}/${BINARY_NAME}"
