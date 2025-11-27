Local 3-Chain + Hermes E2E (StayOnComet)

What this provides
- 3 Cosmos-SDK chains: gm-a, gm-b, gm-c (single validator each by default)
- One Hermes relayer configured for all chains
- ICS20 channels between A<->B and A<->C
- A governance-driven migration on chain A with StayOnComet=true
- A Python backfill script to advance IBC clients across the smoothing window
- Simple transfer verification after the migration

Quick start
1) Spin up containers
   - From repo root: docker compose -f scripts/e2e/docker-compose.yml up -d

2) Initialize chains (single validator each) and start nodes
   - scripts/e2e/init-chain.sh gm-a-val-0 gm-a 26657 9090
   - scripts/e2e/init-chain.sh gm-b-val-0 gm-b 26657 9090
   - scripts/e2e/init-chain.sh gm-c-val-0 gm-c 26657 9090

3) Configure Hermes and add keys
   - scripts/e2e/hermes-init.sh

4) Fund Hermes relayer addresses and create IBC channels (A<->B, A<->C)
   - scripts/e2e/ibc-setup.sh

5) Submit StayOnComet migration proposal on A
   - scripts/e2e/migrate-stayoncomet.sh 30
   - Wait until chain A reaches migration_height + window

6) Backfill client updates across the window
   - python3 scripts/e2e/update_ibc_clients_backfill.py gm-a gm-b gm-c --migration-height <MIG_FROM_STEP_5> --window 30

7) Verify IBC with a test transfer (e.g., from gm-a to gm-b)
   - Use gmd tx ibc-transfer transfer ... on gm-a and watch Hermes relay

Notes
- The compose file exposes RPC+gRPC for all chains on host ports:
  - gm-a: RPC 26657, gRPC 9090
  - gm-b: RPC 27657, gRPC 9190
  - gm-c: RPC 28657, gRPC 9290
- This scaffold initializes one validator per chain by default. If you need gm-a with 3 validators, you can extend the initialization to produce a multi-validator genesis and distribute it to gm-a-val-1/2; or start gm-a-val-1/2 as additional nodes and use staking txs to promote them to validators. The StayOnComet overlap logic is compatible with single or multiple validators.

