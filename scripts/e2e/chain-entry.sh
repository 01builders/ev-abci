#!/bin/sh
set -e

CHAIN_ID="${CHAIN_ID:-gm-a}"
RPC_PORT="${RPC_PORT:-26657}"
GRPC_PORT="${GRPC_PORT:-9090}"
DENOM="stake"
HOME_DIR="/home/gm/.gm"
MNEMONIC_FILE="/scripts/relayer.mnemonic"

echo "[chain] init $CHAIN_ID (rpc=$RPC_PORT grpc=$GRPC_PORT)"

mkdir -p "$HOME_DIR" "$HOME_DIR/config"

if [ ! -f "$HOME_DIR/config/genesis.json" ]; then
  gmd init "$HOSTNAME" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

  # faucet key (ensure it exists)
if ! gmd keys show faucet --keyring-backend test --home "$HOME_DIR" >/dev/null 2>&1; then
    gmd keys add faucet --keyring-backend test --home "$HOME_DIR" >/dev/null 2>&1
fi
  FAUCET_ADDR=$(gmd keys show faucet -a --keyring-backend test --home "$HOME_DIR")

  # add faucet genesis account by explicit address
  if gmd genesis add-genesis-account --help --home "$HOME_DIR" >/dev/null 2>&1; then
    gmd genesis add-genesis-account "$FAUCET_ADDR" 100000000000"$DENOM" --home "$HOME_DIR"
  else
    gmd add-genesis-account "$FAUCET_ADDR" 100000000000"$DENOM" --home "$HOME_DIR"
  fi

  # pre-fund relayer account from shared mnemonic (same address on all chains as Hermes)
  if [ -f "$MNEMONIC_FILE" ]; then
    if ! gmd keys show relayer --keyring-backend test --home "$HOME_DIR" >/dev/null 2>&1; then
      # recover relayer key non-interactively from mnemonic file
      gmd keys add relayer --recover --keyring-backend test --home "$HOME_DIR" < "$MNEMONIC_FILE" >/dev/null 2>&1
    fi
    RELAYER_ADDR=$(gmd keys show relayer -a --keyring-backend test --home "$HOME_DIR" 2>/dev/null || true)
    if [ -n "$RELAYER_ADDR" ]; then
      if gmd genesis add-genesis-account --help --home "$HOME_DIR" >/dev/null 2>&1; then
        gmd genesis add-genesis-account "$RELAYER_ADDR" 100000000000"$DENOM" --home "$HOME_DIR"
      else
        gmd add-genesis-account "$RELAYER_ADDR" 100000000000"$DENOM" --home "$HOME_DIR"
      fi
    fi
  fi

  # gentx + collect
  if gmd genesis gentx --help --home "$HOME_DIR" >/dev/null 2>&1; then
    gmd genesis gentx faucet 10000000000"$DENOM" --moniker "$HOSTNAME" --chain-id "$CHAIN_ID" --keyring-backend test --home "$HOME_DIR"
    gmd genesis collect-gentxs --home "$HOME_DIR"
  else
    gmd gentx faucet 10000000000"$DENOM" --moniker "$HOSTNAME" --chain-id "$CHAIN_ID" --keyring-backend test --home "$HOME_DIR"
    gmd collect-gentxs --home "$HOME_DIR"
  fi

  # config
  sed -i "s/^minimum-gas-prices.*/minimum-gas-prices = \"0.0$DENOM\"/" "$HOME_DIR/config/app.toml"
  sed -i "s/^indexer.*/indexer = \"kv\"/" "$HOME_DIR/config/config.toml"
  sed -i "s|127.0.0.1:26657|0.0.0.0:26657|" "$HOME_DIR/config/config.toml"
  sed -i "s|127.0.0.1:9090|0.0.0.0:9090|" "$HOME_DIR/config/app.toml"
fi

echo "[chain] starting $CHAIN_ID"
exec gmd start --home "$HOME_DIR" --rpc.laddr tcp://0.0.0.0:"$RPC_PORT" --grpc.address 0.0.0.0:"$GRPC_PORT"
