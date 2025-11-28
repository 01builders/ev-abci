#!/bin/sh
set -e

CONFIG=/home/hermes/.hermes/config.toml
MNEMONIC=/scripts/relayer.mnemonic

# Ensure config dir exists and is writable
mkdir -p /home/hermes/.hermes || true

# Point Hermes to the generated config for all subsequent CLI calls
export HERMES_HOME=/home/hermes/.hermes
export HERMES_CONFIG="$CONFIG"

cat > "$CONFIG" <<EOF
[global]
log_level = "info"

[mode]
clients = { enabled = true, refresh = true, misbehaviour = true }
connections = { enabled = true }
channels = { enabled = true }
packets = { enabled = true, clear_on_start = true }

[rest]
enabled = true
host = "0.0.0.0"
port = 3000

## Telemetry omitted for simplicity

[[chains]]
id = "gm-a"
rpc_addr = "http://gm-a-val-0:26657"
grpc_addr = "http://gm-a-val-0:9090"
event_source = { mode = "pull", interval = "1s" }
clock_drift = "60s"
trusted_node = false
account_prefix = "gm"
key_name = "relayer-gm-a"
store_prefix = "ibc"
default_gas = 250000
max_gas = 400000
gas_price = { price = 0.0, denom = "stake" }

[[chains]]
id = "gm-b"
rpc_addr = "http://gm-b-val-0:26657"
grpc_addr = "http://gm-b-val-0:9090"
event_source = { mode = "pull", interval = "1s" }
clock_drift = "60s"
trusted_node = false
account_prefix = "gm"
key_name = "relayer-gm-b"
store_prefix = "ibc"
default_gas = 250000
max_gas = 400000
gas_price = { price = 0.0, denom = "stake" }

[[chains]]
id = "gm-c"
rpc_addr = "http://gm-c-val-0:26657"
grpc_addr = "http://gm-c-val-0:9090"
event_source = { mode = "pull", interval = "1s" }
clock_drift = "60s"
trusted_node = false
account_prefix = "gm"
key_name = "relayer-gm-c"
store_prefix = "ibc"
default_gas = 250000
max_gas = 400000
gas_price = { price = 0.0, denom = "stake" }
EOF

## Health checks in docker-compose ensure chains have produced blocks before Hermes starts.

set -e
echo "[hermes] ensuring relayer keys exist"
for ch in gm-a gm-b gm-c; do
  name="relayer-$ch"
  echo "  checking $ch key $name"
  if hermes --config "$CONFIG" keys list --chain "$ch" 2>/dev/null | grep -q "$name"; then
    echo "    [$ch] key $name already present"
    continue
  fi
  echo "    [$ch] importing $name from mnemonic"
  hermes --config "$CONFIG" keys add --chain "$ch" --mnemonic-file "$MNEMONIC" --key-name "$name"
done

# Create connections + channels (idempotent)
echo "[hermes] creating IBC connections/channels"
# A <-> B
hermes --config "$CONFIG" create connection --a-chain gm-a --b-chain gm-b || true
hermes --config "$CONFIG" create channel \
  --a-chain gm-a --a-connection connection-0 \
  --a-port transfer --b-port transfer
# A <-> C
hermes --config "$CONFIG" create connection --a-chain gm-a --b-chain gm-c || true
hermes --config "$CONFIG" create channel \
  --a-chain gm-a --a-connection connection-1 \
  --a-port transfer --b-port transfer

echo "[hermes] starting relayer"
exec hermes --config "$CONFIG" start
