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
  curl -sSL "$DOWNLOAD_URL" -o "${DEST_DIR}/${BINARY_NAME}" 2>/dev/null || true
elif command -v wget >/dev/null 2>&1; then
  wget -qO "${DEST_DIR}/${BINARY_NAME}" "$DOWNLOAD_URL" 2>/dev/null || true
fi

if [ ! -s "${DEST_DIR}/${BINARY_NAME}" ]; then
  echo "Release binary download failed. Falling back to 'go install'..."
  if command -v go >/dev/null 2>&1; then
    go install "github.com/${REPO}@latest"
    echo "Installed via go install successfully."
    exit 0
  else
    echo "Error: Could not download release binary and 'go' is not installed."
    exit 1
  fi
fi

chmod +x "${DEST_DIR}/${BINARY_NAME}"
echo "${BINARY_NAME} installed successfully to ${DEST_DIR}/${BINARY_NAME}"
