#!/bin/sh
# JEKYO installer — https://jekyo.com
# Detects OS/arch, downloads the latest release binary, installs to PATH.
set -eu

REPO="jekyo/jekyo"
BIN="jekyo"

main() {
  os="$(uname -s)"
  arch="$(uname -m)"

  case "$os" in
    Linux) os="linux" ;;
    Darwin) os="darwin" ;;
    MINGW* | MSYS* | CYGWIN*)
      echo "Windows: download jekyo-windows-amd64.exe from" >&2
      echo "  https://github.com/$REPO/releases/latest" >&2
      exit 1
      ;;
    *)
      echo "Unsupported OS: $os" >&2
      exit 1
      ;;
  esac

  case "$arch" in
    x86_64 | amd64) arch="amd64" ;;
    aarch64 | arm64) arch="arm64" ;;
    *)
      echo "Unsupported architecture: $arch" >&2
      exit 1
      ;;
  esac

  url="https://github.com/$REPO/releases/latest/download/$BIN-$os-$arch"
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT

  echo "Downloading $BIN ($os/$arch)..."
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$tmp"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$tmp" "$url"
  else
    echo "Need curl or wget." >&2
    exit 1
  fi
  chmod +x "$tmp"

  dest="/usr/local/bin"
  if [ -w "$dest" ]; then
    mv "$tmp" "$dest/$BIN"
  elif command -v sudo >/dev/null 2>&1; then
    echo "Installing to $dest (sudo may prompt for your password)..."
    sudo mv "$tmp" "$dest/$BIN"
  else
    dest="$HOME/.local/bin"
    mkdir -p "$dest"
    mv "$tmp" "$dest/$BIN"
    case ":$PATH:" in
      *":$dest:"*) ;;
      *) echo "NOTE: add $dest to your PATH." ;;
    esac
  fi
  trap - EXIT

  echo "Installed: $("$dest/$BIN" version)"
  echo
  echo "Next steps:"
  echo "  jekyo server install user@your-server --ip <ip> --storage /storage"
  echo "  jekyo init && jekyo up"
  echo
  echo "Using an AI agent (Claude Code, Codex, Cursor)?"
  echo "  jekyo skill install --global    # then just say: /jekyo deploy this"
}

main
