#!/usr/bin/env bash
set -euo pipefail

HERMES="docker exec -i hermes hermes"

cat > /tmp/hermes-config.toml <<'EOF'
[global]
resolver = "gethostbyname"
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

[telemetry]
enabled = false

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

docker cp /tmp/hermes-config.toml hermes:/home/hermes/.hermes/config.toml

# create keys and show addresses to fund
for ch in gm-a gm-b gm-c; do
  $HERMES keys add --chain "$ch" --mnemonic-file /dev/null >/dev/null 2>&1 || true
  $HERMES keys list --chain "$ch"
done

echo "[ok] Hermes config written and keys added. Next: run scripts/e2e/ibc-setup.sh"

