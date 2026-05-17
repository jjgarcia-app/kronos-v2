#!/bin/sh
# Instala Kronos — memoria persistente para agentes de IA
# Uso: curl -fsSL https://raw.githubusercontent.com/jjgarcia-app/kronos-v2/main/install.sh | sh
set -e

REPO="jjgarcia-app/kronos-v2"
BINARY="kronos"

# ── Detectar OS ───────────────────────────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin) OS="darwin" ;;
  linux)  OS="linux"  ;;
  *)
    echo "Sistema no soportado: $OS"
    echo "Descarga el binario manualmente desde: https://github.com/$REPO/releases"
    exit 1
    ;;
esac

# ── Detectar arquitectura ─────────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64)   ARCH="amd64" ;;
  arm64|aarch64)  ARCH="arm64" ;;
  *)
    echo "Arquitectura no soportada: $ARCH"
    exit 1
    ;;
esac

# ── Obtener última versión ────────────────────────────────────────────────────
echo "Buscando la última versión de Kronos..."
VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | grep '"tag_name"' | head -1 | cut -d'"' -f4)

if [ -z "$VERSION" ]; then
  echo "Error: no se pudo obtener la última versión."
  echo "Verifica tu conexión o descarga manualmente de: https://github.com/$REPO/releases"
  exit 1
fi

echo "Versión: $VERSION  |  Sistema: $OS/$ARCH"

# ── Descargar ─────────────────────────────────────────────────────────────────
URL="https://github.com/$REPO/releases/download/$VERSION/kronos_${OS}_${ARCH}.tar.gz"
TMP_DIR=$(mktemp -d)
TMP_FILE="$TMP_DIR/kronos.tar.gz"

echo "Descargando $URL..."
curl -fsSL "$URL" -o "$TMP_FILE"
tar -xzf "$TMP_FILE" -C "$TMP_DIR"

# ── Instalar ──────────────────────────────────────────────────────────────────
# Intentar directorio del sistema, si no accesible usar ~/bin
if [ -w "/usr/local/bin" ]; then
  INSTALL_DIR="/usr/local/bin"
elif [ -d "/opt/homebrew/bin" ]; then
  INSTALL_DIR="/opt/homebrew/bin"
else
  INSTALL_DIR="$HOME/.local/bin"
  mkdir -p "$INSTALL_DIR"
  # Asegurar que esté en PATH
  SHELL_RC=""
  case "$SHELL" in
    */zsh)  SHELL_RC="$HOME/.zshrc"  ;;
    */bash) SHELL_RC="$HOME/.bashrc" ;;
  esac
  if [ -n "$SHELL_RC" ] && ! grep -q "$INSTALL_DIR" "$SHELL_RC" 2>/dev/null; then
    echo "export PATH=\"\$PATH:$INSTALL_DIR\"" >> "$SHELL_RC"
    echo "Añadido $INSTALL_DIR al PATH en $SHELL_RC"
  fi
fi

if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP_DIR/$BINARY" "$INSTALL_DIR/$BINARY"
else
  sudo mv "$TMP_DIR/$BINARY" "$INSTALL_DIR/$BINARY"
fi
chmod +x "$INSTALL_DIR/$BINARY"
rm -rf "$TMP_DIR"

echo ""
echo "✓ Kronos $VERSION instalado en $INSTALL_DIR/$BINARY"
echo ""

# ── Lanzar wizard de configuración ───────────────────────────────────────────
if command -v kronos > /dev/null 2>&1; then
  echo "Iniciando configuración..."
  echo ""
  kronos init
else
  echo "Abre una nueva terminal y ejecuta:"
  echo "  kronos init"
fi
