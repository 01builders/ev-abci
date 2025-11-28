SHELL := /bin/bash

COMPOSE := docker compose -f docker-compose.yml

# Default ports exposed in compose
RPC_A ?= 26657
GRPC_A ?= 9090
RPC_B ?= 26657
GRPC_B ?= 9090
RPC_C ?= 26657
GRPC_C ?= 9090

# Migration window and height (set MIG when running backfill)
WINDOW ?= 30
MIG ?=

.PHONY: help
help:
	@echo "E2E orchestration targets:"
	@echo "  make up           - start chains (wait for height >= 1)"
	@echo "  make down         - stop containers"
	@echo "  make ps           - list containers"
	@echo "  make logs-a/b/c   - tail chain logs"
	@echo "  make hermes-start - start Hermes only (after chains ready)"
	@echo "  make ibc-setup    - fund relayer and create A<->B, A<->C channels"
	@echo "  make ibc-transfer - send IBC transfer from gm-a to gm-b (can be called multiple times)"
	@echo "  make ibc-transfer-verify - verify IBC transfer completed"
	@echo "  make migrate      - submit StayOnComet migration on A (WINDOW=$(WINDOW))"
	@echo "  make backfill MIG=<height> [WINDOW=$(WINDOW)] - run client backfill"
	@echo "  make clean        - down and remove volumes"

.PHONY: up
up:
	$(COMPOSE) up -d gm-a-val-0 gm-a-val-1 gm-a-val-2 gm-b-val-0 gm-c-val-0 hermes
	@echo "[wait] waiting for chains to reach height >= 1"
	@bash -c 'set -e; \
		parse_height() { \
		  url="$$1"; \
		  if command -v jq >/dev/null 2>&1; then \
		    curl -s "$$url" | jq -r .result.sync_info.latest_block_height 2>/dev/null || true; \
		  else \
		    curl -s "$$url" | sed -n "s/.*\"latest_block_height\":\"\([0-9]\+\)\".*/\1/p"; \
		  fi; \
		}; \
		endpoints=("http://localhost:26657/status" "http://localhost:27657/status" "http://localhost:28657/status"); \
		for ep in "$${endpoints[@]}"; do \
		  echo " waiting on $$ep"; \
		  ok=0; \
		  for i in $$(seq 1 120); do \
		    h=$$(parse_height $$ep); \
		    if [ -n "$$h" ] && [ "$$h" -ge 1 ] 2>/dev/null; then \
		      echo "  ok height=$$h"; ok=1; break; \
		    fi; \
		    sleep 1; \
		  done; \
		  if [ $$ok -ne 1 ]; then echo " timeout waiting for $$ep" >&2; exit 1; fi; \
		done'
	@echo "[up] Hermes started (gated by chain healthchecks)"

.PHONY: down
down:
	$(COMPOSE) down

.PHONY: ps
ps:
	$(COMPOSE) ps

.PHONY: logs-a logs-b logs-c
logs-a:
	docker logs -f gm-a-val-0
logs-b:
	docker logs -f gm-b-val-0
logs-c:
	docker logs -f gm-c-val-0

.PHONY: init

.PHONY: hermes-start
hermes-start:
	$(COMPOSE) up -d hermes

.PHONY: ibc-setup
ibc-setup:
	bash ./ibc-setup.sh

.PHONY: ibc-transfer
ibc-transfer:
	@echo "[ibc-transfer] Sending 1000000stake from gm-a to gm-b"
	@docker exec -i gm-a-val-0 gmd tx ibc-transfer transfer transfer channel-0 \
		$$(docker exec -i gm-b-val-0 gmd keys show faucet -a --keyring-backend test --home /home/gm/.gm) \
		1000000stake \
		--from faucet \
		--keyring-backend test \
		--chain-id gm-a \
		--home /home/gm/.gm \
		--yes \
		--output json | jq -r '.txhash // "Transaction submitted"'

.PHONY: ibc-transfer-verify
ibc-transfer-verify:
	@echo "[ibc-transfer-verify] Checking IBC balance on gm-b..."
	@sleep 5
	@IBC_DENOM=$$(docker exec -i gm-b-val-0 gmd query bank balances \
		$$(docker exec -i gm-b-val-0 gmd keys show faucet -a --keyring-backend test --home /home/gm/.gm) \
		--home /home/gm/.gm \
		--output json 2>/dev/null | jq -r '.balances[] | select(.denom | startswith("ibc/")) | .denom' | head -1); \
	if [ -n "$$IBC_DENOM" ]; then \
		BALANCE=$$(docker exec -i gm-b-val-0 gmd query bank balances \
			$$(docker exec -i gm-b-val-0 gmd keys show faucet -a --keyring-backend test --home /home/gm/.gm) \
			--home /home/gm/.gm \
			--output json 2>/dev/null | jq -r ".balances[] | select(.denom == \"$$IBC_DENOM\") | .amount"); \
		echo "[ibc-transfer-verify] ✓ IBC balance: $$BALANCE $$IBC_DENOM"; \
	else \
		echo "[ibc-transfer-verify] ✗ No IBC tokens found yet"; \
	fi

.PHONY: migrate
migrate:
	WINDOW=$(WINDOW) bash ./migrate-stayoncomet.sh $(WINDOW)

.PHONY: backfill
backfill:
	@if [ -z "$(MIG)" ]; then echo "Set MIG=<migration_start_height> (e.g., MIG=120)" >&2; exit 2; fi
	python3 ./update_ibc_clients_backfill.py gm-a gm-b gm-c --migration-height $(MIG) --window $(WINDOW)

.PHONY: clean
clean:
	$(COMPOSE) down -v
