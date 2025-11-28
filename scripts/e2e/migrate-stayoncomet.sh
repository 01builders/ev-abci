#!/usr/bin/env bash
set -euo pipefail

# migrate to single validator (validator-0) while staying on CometBFT
# hardcoded for gm-a-val-0 container

NODE="gm-a-val-0"
CHAIN_ID="gm-a"
KEY="faucet"
HOME_DIR="/home/gm/.gm"
VALIDATORS=("gm-a-val-0" "gm-a-val-1" "gm-a-val-2")

echo "[migrate-stayoncomet] getting current height..."
CURRENT_HEIGHT=$(docker exec "$NODE" sh -lc "gmd status --home $HOME_DIR" | grep -o '"latest_block_height":"[0-9]*"' | grep -o '[0-9]*')
MIGRATE_AT=$((CURRENT_HEIGHT + 30))

echo "[migrate-stayoncomet] current height: $CURRENT_HEIGHT, migration at: $MIGRATE_AT"

echo "[migrate-stayoncomet] getting validator-0 pubkey..."
VALIDATOR_PUBKEY=$(docker exec "$NODE" sh -lc "gmd query staking validators --home $HOME_DIR --output json" | grep -A 2 '"consensus_pubkey"' | grep '"value"' | head -1 | sed 's/.*"value": "\(.*\)".*/\1/')

echo "[migrate-stayoncomet] validator pubkey: $VALIDATOR_PUBKEY"

echo "[migrate-stayoncomet] using hardcoded gov module address..."
GOV_ADDRESS="gm10d07y265gmmuvt4z0w9aw880jnsr700j5nsal6"

echo "[migrate-stayoncomet] gov address: $GOV_ADDRESS"

echo "[migrate-stayoncomet] creating proposal json..."
cat > /tmp/proposal.json <<EOF
{
  "messages": [
    {
      "@type": "/evabci.migrationmngr.v1.MsgMigrateToEvolve",
      "authority": "$GOV_ADDRESS",
      "block_height": "$MIGRATE_AT",
      "sequencer": {
        "name": "validator-0",
        "consensus_pubkey": {
          "@type": "/cosmos.crypto.ed25519.PubKey",
          "key": "$VALIDATOR_PUBKEY"
        }
      },
      "attesters": [],
      "stay_on_comet": true
    }
  ],
  "metadata": "",
  "deposit": "10000000stake",
  "title": "Migrate to Single Validator (Stay on Comet)",
  "summary": "Reduce validator set to single validator while staying on CometBFT"
}
EOF

echo "[migrate-stayoncomet] copying proposal to container..."
docker cp /tmp/proposal.json "$NODE":/tmp/proposal.json

echo "[migrate-stayoncomet] submitting governance proposal..."
docker exec "$NODE" sh -lc "gmd tx gov submit-proposal /tmp/proposal.json \
  --from $KEY \
  --keyring-backend test \
  --chain-id $CHAIN_ID \
  --home $HOME_DIR \
  --yes \
  --output json" > /tmp/submit_output.json 2>&1

SUBMIT_OUTPUT=$(cat /tmp/submit_output.json)
echo "[migrate-stayoncomet] submit output: $SUBMIT_OUTPUT"

TXHASH=$(echo "$SUBMIT_OUTPUT" | grep -o '"txhash":"[^"]*"' | cut -d'"' -f4)
echo "[migrate-stayoncomet] tx hash: $TXHASH"

echo "[migrate-stayoncomet] waiting for tx to be committed..."
sleep 5

echo "[migrate-stayoncomet] querying proposal ID from transaction..."
PROPOSAL_ID=$(docker exec "$NODE" sh -lc "gmd query tx $TXHASH --home $HOME_DIR --output json 2>/dev/null" | grep -o '"key":"proposal_id","value":"[0-9]*"' | grep -o '[0-9]*' | head -1)

if [ -z "$PROPOSAL_ID" ]; then
  echo "[migrate-stayoncomet] failed to extract proposal ID, querying all proposals..."
  sleep 3
  PROPOSAL_ID=$(docker exec "$NODE" sh -lc "gmd query gov proposals --home $HOME_DIR --output json" | grep -o '\"id\":\"[0-9]*\"' | tail -1 | grep -o '[0-9]*')
fi

echo "[migrate-stayoncomet] proposal ID: $PROPOSAL_ID"

echo "[migrate-stayoncomet] waiting for proposal to be queryable..."
sleep 3

echo "[migrate-stayoncomet] voting yes on proposal from all validators..."
for val in "${VALIDATORS[@]}"; do
  echo "[migrate-stayoncomet] voting from $val..."
  if docker exec "$val" sh -lc "gmd tx gov vote $PROPOSAL_ID yes \
    --from $KEY \
    --keyring-backend test \
    --chain-id $CHAIN_ID \
    --home $HOME_DIR \
    --yes" 2>&1 | grep -q "code: 0"; then
    echo "[migrate-stayoncomet] ✓ vote from $val succeeded"
  else
    echo "[migrate-stayoncomet] ✗ vote from $val failed"
  fi
  sleep 2
done

echo "[migrate-stayoncomet] waiting for votes to be processed..."
sleep 3

echo "[migrate-stayoncomet] proposal submitted and voted on"
echo "[migrate-stayoncomet] proposal ID: $PROPOSAL_ID"
echo "[migrate-stayoncomet] migration will execute at height $MIGRATE_AT"
