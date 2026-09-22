#!/usr/bin/env bash
# TgWebDAV installer — downloads the latest release binary for your arch.
#   sudo bash install.sh              -> install latest to /usr/local/bin
#   sudo bash install.sh v1.2.3       -> install a specific version
set -euo pipefail

REPO="Sudantha123/Tg-webdav2"
BIN_NAME="tgwebdav"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

if [[ $EUID -ne 0 ]]; then
  echo "Run with sudo (installs to $INSTALL_DIR)."; exit 1
fi

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  TARCH="amd64" ;;
  aarch64|arm64) TARCH="arm64" ;;
  *) echo "Unsupported arch: $ARCH"; exit 1 ;;
esac

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"tag_name": *"//;s/"$//')
fi
[[ -n "$VERSION" ]] || { echo "Could not resolve latest version"; exit 1; }

URL="https://github.com/${REPO}/releases/download/${VERSION}/${BIN_NAME}-linux-${TARCH}"
echo "Downloading ${URL}"
curl -fL --progress-bar -o "${INSTALL_DIR}/${BIN_NAME}" "$URL"
chmod +x "${INSTALL_DIR}/${BIN_NAME}"
"${INSTALL_DIR}/${BIN_NAME}" --version

cat <<EOF

Installed! Next steps:
  1. mkdir -p /opt/tgwebdav && cd /opt/tgwebdav
  2. cp your .env here (see .env.example in the repo)
  3. Run:  ENV_FILE=/opt/tgwebdav/.env DATA_DIR=/opt/tgwebdav/data ${INSTALL_DIR}/${BIN_NAME}
  4. (optional) install the systemd service, see README.md

EOF
