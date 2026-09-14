#!/usr/bin/env bash
#
# deploy-back.sh — Compila la API Go de El Centinela y la despliega en PRUEBAS o ESTABLE.
#
# Flujo:
#   1. Empaqueta el repositorio local y lo copia al host Proxmox.
#   2. Compila con /opt/go/bin/go en el host.
#   3. Instala el binario en el contenedor del entorno elegido.
#   4. Reinicia el servicio systemd centinela-api y verifica que quede activo.
#
# Requisitos:
#   - Acceso SSH al host Proxmox con la clave indicada.
#   - Toolchain Go en el host (/opt/go). Ajustable con GO_BIN.
#   - El servicio centinela-api y su EnvironmentFile ya existen en el contenedor.
#
# Uso:
#   ./scripts/deploy-back.sh test
#   ./scripts/deploy-back.sh stable
#   SSH_KEY=~/.ssh/mi_clave ./scripts/deploy-back.sh test
#
# Variables (opcionales, con default razonable):
#   SSH_KEY     Clave SSH privada para el host Proxmox.
#   SSH_USER    Usuario SSH del host Proxmox.      (default: root)
#   HOST        Alias/host del Proxmox.        (default: proxmox)
#   TEST_CT     Contenedor de PRUEBAS.         (default: 102)
#   STABLE_CT   Contenedor de ESTABLE.         (default: 104)
#   APP_DIR     Directorio de la app en el CT. (default: /opt/centinela-api)
#   SERVICE     Nombre del servicio systemd.   (default: centinela-api)
#   GO_BIN      Ruta del binario go.           (default: /opt/go/bin/go)

set -euo pipefail

ENVIRONMENT="${1:-}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/opencode_centinela}"
SSH_USER="${SSH_USER:-root}"
HOST="${HOST:-proxmox}"
TEST_CT="${TEST_CT:-102}"
STABLE_CT="${STABLE_CT:-104}"
APP_DIR="${APP_DIR:-/opt/centinela-api}"
SERVICE="${SERVICE:-centinela-api}"
GO_BIN="${GO_BIN:-/opt/go/bin/go}"
BINARY_NAME="centinela-api"

case "$ENVIRONMENT" in
  test)   CT="$TEST_CT" ;;
  stable) CT="$STABLE_CT" ;;
  *) echo "Uso: $0 {test|stable}" >&2; exit 2 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_SRC="${APP_SRC:-$(cd "$SCRIPT_DIR/.." && pwd)}"
REMOTE_DIR="/tmp/centinela-back-build"
REMOTE_TAR="/tmp/centinela-back-src.tar.gz"
LOCAL_TAR="$(mktemp -t centinela-back-src.XXXXXX.tar.gz)"

log()  { printf '\033[1;34m[deploy-back]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[deploy-back] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }
SSH_OPTS=(-i "$SSH_KEY" -o BatchMode=yes -o ConnectTimeout=15)
SSH_TARGET="$SSH_USER@$HOST"

cleanup() {
  rm -f "$LOCAL_TAR"
  ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "rm -rf '$REMOTE_DIR' '$REMOTE_TAR' /tmp/$BINARY_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# --- 0. Validaciones locales -------------------------------------------------
[ -f "$APP_SRC/go.mod" ] || fail "No se encontró go.mod en $APP_SRC"
[ -d "$APP_SRC/cmd/api" ] || fail "No se encontró cmd/api en $APP_SRC"

# --- 1. Empaquetar y copiar al host -----------------------------------------
log "Empaquetando $APP_SRC y copiando a $SSH_TARGET"
tar -czf "$LOCAL_TAR" -C "$APP_SRC" --exclude=.git --exclude=dist --exclude=node_modules .
scp "${SSH_OPTS[@]}" "$LOCAL_TAR" "$SSH_TARGET:$REMOTE_TAR" >/dev/null

# --- 2. Compilar en el host --------------------------------------------------
log "Compilando con $GO_BIN en $SSH_TARGET"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "
  set -e
  rm -rf '$REMOTE_DIR'
  mkdir -p '$REMOTE_DIR'
  tar xzf '$REMOTE_TAR' -C '$REMOTE_DIR'
  cd '$REMOTE_DIR'
  '$GO_BIN' build -trimpath -o /tmp/$BINARY_NAME ./cmd/api
  test -x /tmp/$BINARY_NAME
"

# --- 3. Instalar en el contenedor -------------------------------------------
log "Instalando en CT $CT ($ENVIRONMENT):$APP_DIR"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "
  set -e
  # Respaldo del binario previo (solo la primera vez; no pisa un backup existente).
  pct exec '$CT' -- sh -c 'cp -n \"$APP_DIR/$BINARY_NAME\" \"$APP_DIR/$BINARY_NAME.bak\" 2>/dev/null || true'
  pct push '$CT' /tmp/$BINARY_NAME '$APP_DIR/$BINARY_NAME'
  pct exec '$CT' -- chmod 755 '$APP_DIR/$BINARY_NAME'
"

# --- 4. Reiniciar y verificar el servicio -----------------------------------
log "Reiniciando $SERVICE en CT $CT"
ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "
  set -e
  pct exec '$CT' -- systemctl restart '$SERVICE'
  sleep 2
  pct exec '$CT' -- systemctl is-active '$SERVICE'
  pct exec '$CT' -- systemctl --no-pager --lines=5 status '$SERVICE' || true
"

log "Despliegue de API completado en CT $CT ($ENVIRONMENT)"
