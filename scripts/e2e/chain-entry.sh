#!/bin/sh
set -e

CHAIN_ID="${CHAIN_ID:-gm-a}"
RPC_PORT="${RPC_PORT:-26657}"
GRPC_PORT="${GRPC_PORT:-9090}"
DENOM="stake"
HOME_DIR="/home/gm/.gm"
MNEMONIC_FILE="/scripts/relayer.mnemonic"

echo "[chain] init $CHAIN_ID (rpc=$RPC_PORT grpc=$GRPC_PORT)"

# initialize chain
gmd init "$HOSTNAME" --chain-id "$CHAIN_ID" --home "$HOME_DIR"

# create faucet key
gmd keys add faucet --keyring-backend test --home "$HOME_DIR" >/dev/null 2>&1
FAUCET_ADDR=$(gmd keys show faucet -a --keyring-backend test --home "$HOME_DIR")

# add faucet as genesis account
gmd genesis add-genesis-account "$FAUCET_ADDR" 100000000000"$DENOM" --home "$HOME_DIR"

# recover relayer key from shared mnemonic
gmd keys add relayer --recover --keyring-backend test --home "$HOME_DIR" < "$MNEMONIC_FILE" >/dev/null 2>&1
RELAYER_ADDR=$(gmd keys show relayer -a --keyring-backend test --home "$HOME_DIR")

# add relayer as genesis account
gmd genesis add-genesis-account "$RELAYER_ADDR" 100000000000"$DENOM" --home "$HOME_DIR"

# create genesis transaction
gmd genesis gentx faucet 10000000000"$DENOM" --moniker "$HOSTNAME" --chain-id "$CHAIN_ID" --keyring-backend test --home "$HOME_DIR"
gmd genesis collect-gentxs --home "$HOME_DIR"

# configure app
sed -i "s/^minimum-gas-prices.*/minimum-gas-prices = \"0.0$DENOM\"/" "$HOME_DIR/config/app.toml"
sed -i "s/^indexer.*/indexer = \"kv\"/" "$HOME_DIR/config/config.toml"
sed -i "s|127.0.0.1:26657|0.0.0.0:26657|" "$HOME_DIR/config/config.toml"
sed -i "s|127.0.0.1:9090|0.0.0.0:9090|" "$HOME_DIR/config/app.toml"

# reduce voting period for testing (30 seconds instead of 48 hours)
sed -i "s/\"voting_period\": \"172800s\"/\"voting_period\": \"30s\"/" "$HOME_DIR/config/genesis.json"
sed -i "s/\"expedited_voting_period\": \"86400s\"/\"expedited_voting_period\": \"15s\"/" "$HOME_DIR/config/genesis.json"
sed -i "s/\"max_deposit_period\": \"172800s\"/\"max_deposit_period\": \"30s\"/" "$HOME_DIR/config/genesis.json"

echo "[chain] starting $CHAIN_ID"
exec gmd start --home "$HOME_DIR" --rpc.laddr tcp://0.0.0.0:"$RPC_PORT" --grpc.address 0.0.0.0:"$GRPC_PORT"
