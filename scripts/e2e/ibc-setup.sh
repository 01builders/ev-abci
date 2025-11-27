#!/usr/bin/env bash
set -euo pipefail

HERMES="docker exec -i hermes hermes --config /home/hermes/.hermes/config.toml"

fund() {
  local node="$1" chain_id="$2" key_name="$3"
  local addr
  addr=$($HERMES keys list --chain "$chain_id" | awk '/address/ {print $2; exit}' | tr -d '\r')
  echo "[fund] $chain_id -> $addr"
  docker exec -i "$node" sh -lc "gmd tx bank send faucet $addr 500000000stake --chain-id $chain_id --keyring-backend test --yes"
}

# fund relayer addresses
fund gm-a-val-0 gm-a relayer-gm-a
fund gm-b-val-0 gm-b relayer-gm-b
fund gm-c-val-0 gm-c relayer-gm-c

# give it a few blocks
sleep 3

# create connections + channels
$HERMES create connection --a-chain gm-a --b-chain gm-b
$HERMES create channel --a-chain gm-a --a-port transfer --b-port transfer --order unordered --channel-version ics20-1

$HERMES create connection --a-chain gm-a --b-chain gm-c
$HERMES create channel --a-chain gm-a --a-port transfer --b-port transfer --order unordered --channel-version ics20-1

echo "[ok] IBC connections and channels created between A<->B and A<->C"
