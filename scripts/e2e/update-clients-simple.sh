#!/bin/bash
set -e

CONFIG=/home/hermes/.hermes/config.toml

# Simple IBC client update script - just updates to H+1 and lets Hermes handle bisection
# Usage: ./update-clients-simple.sh <migration_height>

log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
}

if [ -z "$1" ]; then
  echo "Usage: $0 <migration_height>" >&2
  echo "  Updates all IBC clients to migration_height+1" >&2
  exit 2
fi

MIG_HEIGHT="$1"
TARGET_HEIGHT=$((MIG_HEIGHT + 1))

log "Updating IBC clients to height $TARGET_HEIGHT (migration at $MIG_HEIGHT)"

# Query all clients on gm-b that track gm-a
log "Querying clients on gm-b tracking gm-a..."
GMA_CLIENT_ON_B=$(docker exec hermes hermes --json --config $CONFIG query clients --reference-chain gm-a --host-chain gm-b 2>/dev/null | jq -r '.result[0] // empty')

if [ -n "$GMA_CLIENT_ON_B" ]; then
  log "Updating gm-a client on gm-b: $GMA_CLIENT_ON_B -> height $TARGET_HEIGHT"
  docker exec hermes hermes --config $CONFIG  update client --host-chain gm-b --client "$GMA_CLIENT_ON_B" --height "$TARGET_HEIGHT" || {
    log "Warning: Failed to update client $GMA_CLIENT_ON_B on gm-b"
  }
else
  log "No gm-a client found on gm-b"
fi

# Query all clients on gm-c that track gm-a
log "Querying clients on gm-c tracking gm-a..."
GMA_CLIENT_ON_C=$(docker exec hermes hermes --json --config $CONFIG query clients --reference-chain gm-a --host-chain gm-c  2>/dev/null | jq -r '.result[0] // empty')

if [ -n "$GMA_CLIENT_ON_C" ]; then
  log "Updating gm-a client on gm-c: $GMA_CLIENT_ON_C -> height $TARGET_HEIGHT"
  docker exec hermes hermes --config $CONFIG update client --host-chain gm-c --client "$GMA_CLIENT_ON_C" --height "$TARGET_HEIGHT" || {
    log "Warning: Failed to update client $GMA_CLIENT_ON_C on gm-c"
  }
else
  log "No gm-a client found on gm-c"
fi

log "Done! Hermes handled bisection internally."
