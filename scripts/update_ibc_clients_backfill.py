#!/usr/bin/env python3

import argparse
import json
import subprocess
import sys
import logging
from typing import Dict, List, Tuple, Set

logger = logging.getLogger("update_ibc_clients")


def _log(level: int, msg: str) -> None:
    logger.log(level, msg)

def _run_hermes(args: List[str], timeout: float = 60.0) -> Dict:
    cmd = ["hermes", "--json"] + args
    res = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)
    if res.returncode != 0:
        err = (res.stderr or res.stdout or "").strip()
        raise subprocess.CalledProcessError(res.returncode, cmd, output=res.stdout, stderr=err)
    try:
        return json.loads(res.stdout)
    except json.JSONDecodeError as e:
        raise RuntimeError(
            f"failed to decode hermes JSON for {cmd}: {e}\nstdout:\n{res.stdout}\nstderr:\n{res.stderr}"
)

def _query_channels(chain: str) -> List[dict]:
    data = _run_hermes(["query", "channels", "--chain", chain], timeout=90.0)
    result = data.get("result", [])
    return result if isinstance(result, list) else []

def _query_connection_end(chain: str, connection: str) -> Dict:
    return _run_hermes(["query", "connection", "end", "--chain", chain, "--connection", connection])

def _extract_client_ids(conn_end: Dict) -> Tuple[str, str]:
    # Returns (client_id_on_A, client_id_on_B) (B is counterparty's client on host chain)
    conn = conn_end.get("result", {}).get("connection", {})
    end = conn.get("end", {})
    client_a = end.get("client_id") or conn.get("client_id") or ""
    counterparty = end.get("counterparty") or conn.get("counterparty") or {}
    client_b = counterparty.get("client_id") or ""
    return client_a, client_b

def _query_client_state(chain_id: str, client_id: str) -> Dict:
    return _run_hermes(["query", "client", "state", "--chain", chain_id, "--client", client_id])

def _extract_chain_id(client_state: Dict) -> str:
    # Hermes variants: client_state or ClientState
    result = client_state.get("result") or []
    if not isinstance(result, list) or not result:
        return ""
    entry = result[0]
    cs = entry.get("client_state") or entry.get("ClientState") or {}
    return cs.get("chain_id") or ""

def _extract_revision_height(client_state: Dict) -> int:
    # Parse latest_height.revision_height as int
    result = client_state.get("result", [])
    if not isinstance(result, list) or not result:
        return 0
    entry = result[0]
    client_state = entry.get("client_state") or entry.get("ClientState") or {}
    latest_height = client_state.get("latest_height") or {}
    revision_height = latest_height.get("revision_height")
    if isinstance(revision_height, int):
        return revision_height
    if isinstance(revision_height, str) and revision_height.isdigit():
        return int(revision_height)
    try:
        return int(str(revision_height))
    except Exception:
        _log(logging.WARN, f"failed to parse latest_height.revision_height: {revision_height}")
        return 0

def _update_client(host_chain: str, client_id: str, height: int = None) -> None:
    args = ["update", "client", "--host-chain", host_chain, "--client", client_id]
    if height is not None:
        args += ["--height", str(height)]
    _run_hermes(args, timeout=120.0)

def _discover_host_clients(chains: List[str]) -> List[Tuple[str, str, str]]:
    """
    Returns unique triples: (host_chain, client_id_on_host, subject_chain_id).
    host_chain = where the client lives (counterparty), subject_chain_id = chain tracked by client.
    """
    seen: Set[Tuple[str, str]] = set()
    triples: List[Tuple[str, str, str]] = []

    for chain in chains:
        _log(logging.INFO, f"scanning channels on {chain}")
        try:
            channels = _query_channels(chain)
        except Exception as e:
            _log(logging.WARN, f"failed to query channels on {chain}: {e}")
            continue

        for idx, channel in enumerate(channels):
            hops = channel.get("connection_hops") or []
            port = channel.get("port_id") or ""
            chan = channel.get("channel_id") or ""
            if not hops or not port or not chan:
                _log(logging.WARN, f"{chain} channel[{idx}] missing connection/port/channel, skipping")
                continue

            conn_a = hops[0]
            _log(logging.INFO, f"{chain} {port}/{chan} via {conn_a}")

            try:
                conn_end = _query_connection_end(chain, conn_a)
            except Exception as e:
                _log(logging.WARN, f"failed to query connection end {conn_a} on {chain}: {e}")
                continue

            client_id_a, client_id_b = _extract_client_ids(conn_end)
            if not client_id_a or not client_id_b:
                _log(logging.WARN, f"missing client IDs for {chain}/{conn_a}; skipping")
                continue

            try:
                client_state = _query_client_state(chain, client_id_a)
            except Exception as e:
                _log(logging.WARN, f"failed to query client state {client_id_a} on {chain}: {e}")
                continue

            subject_chain = _extract_chain_id(client_state)
            if not subject_chain:
                _log(logging.WARN, f"could not determine counterparty chain-id for {chain}/{conn_a} (client {client_id_a}); skipping")
                continue

            host_chain = subject_chain  # update on the counterparty
            key = (host_chain, client_id_b)
            if key in seen:
                continue
            seen.add(key)
            triples.append((host_chain, client_id_b, subject_chain))
            _log(logging.INFO, f"discovered host={host_chain} client={client_id_b} (tracks {subject_chain})")

    return triples

def _backfill_client(host_chain: str, client_id: str, end_height: int) -> None:
    # Query trusted height on the host client
    try:
        client_state = _query_client_state(host_chain, client_id)
    except Exception as e:
        _log(logging.ERROR, f"query client state failed on host={host_chain} client={client_id}: {e}")
        return

    trusted_height = _extract_revision_height(client_state)
    if trusted_height <= 0:
        _log(logging.WARN, f"could not parse trusted height (got {trusted_height}) on host={host_chain} client={client_id}")

    start = trusted_height + 1
    if start > end_height:
        _log(logging.INFO, f"host={host_chain} client={client_id}: already >= end ({trusted_height} >= {end_height}). skipping")
        return

    _log(logging.INFO, f"host={host_chain} client={client_id}: backfill {start}..{end_height}")
    failures = 0
    for h in range(start, end_height + 1):
        try:
            _update_client(host_chain, client_id, height=h)
        except subprocess.CalledProcessError as e:
            failures += 1
            _log(logging.ERROR, f"update --height {h} failed for host={host_chain} client={client_id}: {e.stderr or e.output}")
            continue
        except Exception as e:
            failures += 1
            _log(logging.ERROR, f"update --height {h} failed for host={host_chain} client={client_id}: {e}")
            continue

    if failures == 0:
        _log(logging.INFO, f"host={host_chain} client={client_id}: backfill complete")
    else:
        _log(logging.WARN, f"host={host_chain} client={client_id}: backfill complete with {failures} failures")

def main() -> None:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s: %(message)s",
    )

    ap = argparse.ArgumentParser(
        description="Backfill IBC UpdateClient from trusted+1 to migrationHeight+window for all connected channels' counterparties using Hermes."
    )
    ap.add_argument("chains", nargs="+", help="Chain IDs to scan (e.g., gm-1 gm-2)")
    ap.add_argument("--migration-height", type=int, required=True, help="Block height where smoothing starts")
    ap.add_argument("--window", type=int, default=30, help="Smoothing window length (default: 30)")
    args = ap.parse_args()

    end_height =  args.migration_height + args.window
    if end_height <= 0:
        _log(logging.ERROR, "end height must be positive")
        sys.exit(2)

    triples = _discover_host_clients(args.chains)
    if not triples:
        _log(logging.WARN, "no host clients discovered; nothing to do")
        return

    for host_chain, client_id, _subject_chain in triples:
        _backfill_client(host_chain, client_id, end_height)

    _log(logging.INFO, "backfill attempted for all discovered host clients")

if __name__ == "__main__":
    main()
