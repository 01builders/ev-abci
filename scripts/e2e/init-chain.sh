#!/usr/bin/env bash
set -euo pipefail

# Initialize and start a single-validator chain inside a container.
# Usage: ./init-chain.sh <container> <chain-id> [rpc_port=26657] [grpc_port=9090]

NODE="${1:-}"
CHAIN_ID="${2:-}"
RPC_PORT="${3:-26657}"
GRPC_PORT="${4:-9090}"

if [[ -z "$NODE" || -z "$CHAIN_ID" ]]; then
  echo "usage: $0 <container> <chain-id> [rpc_port] [grpc_port]" >&2
  exit 2
fi

DENOM=stake

echo "[init] $NODE ($CHAIN_ID)"
docker exec -u 0 -i "$NODE" sh -lc '
set -e
HOME_DIR=/home/gm/.gm
mkdir -p "$HOME_DIR"
chmod -R 777 "$HOME_DIR" || true
if [ ! -d "$HOME_DIR/config" ]; then
  gmd init '"$NODE"' --chain-id '"$CHAIN_ID"' --home "$HOME_DIR"
fi

# faucet key
if ! gmd keys show faucet --keyring-backend test --home "$HOME_DIR" >/dev/null 2>&1; then
  gmd keys add faucet --keyring-backend test --home "$HOME_DIR" >/dev/null 2>&1
fi

FAUCET_ADDR=$(gmd keys show faucet -a --keyring-backend test --home "$HOME_DIR")
# fund faucet (prefer new genesis subcommand, fallback to legacy top-level)
if gmd genesis add-genesis-account --help --home "$HOME_DIR" >/dev/null 2>&1; then
  gmd genesis add-genesis-account "$FAUCET_ADDR" 100000000000'"$DENOM"' --home "$HOME_DIR"
elif gmd add-genesis-account --help --home "$HOME_DIR" >/dev/null 2>&1; then
  gmd add-genesis-account "$FAUCET_ADDR" 100000000000'"$DENOM"' --home "$HOME_DIR"
fi

# gentx if not present (handle SDK CLI variants)
if [ -z "$(ls -1 "$HOME_DIR"/config/gentx 2>/dev/null | head -n1)" ]; then
  if gmd genesis gentx --help --home "$HOME_DIR" >/dev/null 2>&1; then
    gmd genesis gentx faucet --amount 10000000000'"$DENOM"' --chain-id '"$CHAIN_ID"' --keyring-backend test --home "$HOME_DIR"
  elif gmd gentx --help --home "$HOME_DIR" >/dev/null 2>&1; then
    gmd gentx faucet 10000000000'"$DENOM"' --chain-id '"$CHAIN_ID"' --keyring-backend test --home "$HOME_DIR"
  fi
fi

# collect gentxs (prefer new genesis subcommand)
if gmd genesis collect-gentxs --help --home "$HOME_DIR" >/dev/null 2>&1; then
  gmd genesis collect-gentxs --home "$HOME_DIR"
elif gmd collect-gentxs --help --home "$HOME_DIR" >/dev/null 2>&1; then
  gmd collect-gentxs --home "$HOME_DIR"
fi

# app/config tweaks
sed -i "s/^minimum-gas-prices.*/minimum-gas-prices = \"0.0'"$DENOM"'\"/" "$HOME_DIR"/config/app.toml
sed -i "s/^indexer.*/indexer = \"kv\"/" "$HOME_DIR"/config/config.toml
sed -i "s|127.0.0.1:26657|0.0.0.0:26657|" "$HOME_DIR"/config/config.toml
sed -i "s|127.0.0.1:9090|0.0.0.0:9090|" "$HOME_DIR"/config/app.toml

# reduce voting period for testing (30 seconds instead of 48 hours)
sed -i "s/\"voting_period\": \"172800s\"/\"voting_period\": \"30s\"/" "$HOME_DIR"/config/genesis.json
sed -i "s/\"expedited_voting_period\": \"86400s\"/\"expedited_voting_period\": \"15s\"/" "$HOME_DIR"/config/genesis.json
sed -i "s/\"max_deposit_period\": \"172800s\"/\"max_deposit_period\": \"30s\"/" "$HOME_DIR"/config/genesis.json

# start
nohup gmd start --home "$HOME_DIR" --rpc.laddr tcp://0.0.0.0:'"$RPC_PORT"' --grpc.address 0.0.0.0:'"$GRPC_PORT"' \
  >/home/gm/node.log 2>&1 &
echo started
'
