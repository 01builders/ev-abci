#!/usr/bin/env python3
import argparse
import json
import subprocess
import sys
import time
from typing import List, Tuple, Set


def log(msg: str) -> None:
    ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    print(f"{ts} {msg}", file=sys.stderr)


def run_hermes(args: List[str], timeout: float = 60.0) -> dict:
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


def ensure_success(obj: dict, ctx: str) -> None:
    st = obj.get("status")
    if st and str(st).lower() != "success":
        raise RuntimeError(f"{ctx}: status={st}")


def query_channels(chain: str) -> List[dict]:
    data = run_hermes(["query", "channels", "--chain", chain], timeout=90.0)
    ensure_success(data, f"query channels {chain}")
    result = data.get("result", [])
    return result if isinstance(result, list) else []


def query_connection_end(chain: str, connection: str) -> dict:
    data = run_hermes(["query", "connection", "end", "--chain", chain, "--connection", connection])
    ensure_success(data, f"query connection end {chain}/{connection}")
    return data


def extract_client_ids(conn_end: dict) -> Tuple[str, str]:
    # Returns (client_id_on_A, client_id_on_B) (B is counterparty's client on host chain)
    root = conn_end.get("result") or {}
    conn = root.get("connection") or {}
    end = conn.get("end") or {}
    client_a = end.get("client_id") or conn.get("client_id") or ""
    cp = end.get("counterparty") or conn.get("counterparty") or {}

    if not client_a or not cp.get("client_id"):
        # Fallback: connection_end
        alt = root.get("connection_end") or {}
        end2 = alt or {}
        client_a = client_a or end2.get("client_id") or ""
        cp = cp or end2.get("counterparty") or {}

    client_b = cp.get("client_id") or ""
    return client_a, client_b


def query_client_state(chain: str, client_id: str) -> dict:
    data = run_hermes(["query", "client", "state", "--chain", chain, "--client", client_id])
    ensure_success(data, f"query client state {chain}/{client_id}")
    return data


def extract_chain_id(client_state: dict) -> str:
    result = client_state.get("result") or []
    if not isinstance(result, list) or not result:
        return ""
    entry = result[0]
    cs = entry.get("client_state") or entry.get("ClientState") or {}
    return cs.get("chain_id") or ""


def extract_revision_height(client_state: dict) -> int:
    result = client_state.get("result") or []
    if not isinstance(result, list) or not result:
        return 0
    entry = result[0]
    cs = entry.get("client_state") or entry.get("ClientState") or {}
    lh = cs.get("latest_height") or {}
    rh = lh.get("revision_height")
    if isinstance(rh, int):
        return rh
    if isinstance(rh, str) and rh.isdigit():
        return int(rh)
    try:
        return int(str(rh))
    except Exception:
        return 0


def update_client(host_chain: str, client_id: str, height: int = None) -> None:
    args = ["update", "client", "--host-chain", host_chain, "--client", client_id]
    if height is not None:
        args += ["--height", str(height)]
    run_hermes(args, timeout=120.0)


def discover_host_clients(chains: List[str]) -> List[Tuple[str, str, str]]:
    """
    Returns unique triples: (host_chain, client_id_on_host, subject_chain_id).
    host_chain = where the client lives (counterparty), subject_chain_id = chain tracked by client.
    """
    seen: Set[Tuple[str, str]] = set()
    triples: List[Tuple[str, str, str]] = []

    for chain in chains:
        log(f"[info] scanning channels on {chain}")
        try:
            channels = query_channels(chain)
        except Exception as e:
            log(f"[warn] failed to query channels on {chain}: {e}")
            continue

        for idx, ch in enumerate(channels):
            hops = ch.get("connection_hops") or []
            port = ch.get("port_id") or ""
            chan = ch.get("channel_id") or ""
            if not hops or not port or not chan:
                log(f"[warn] {chain} channel[{idx}] missing connection/port/channel; skipping")
                continue
            connA = hops[0]
            log(f"[info] {chain} {port}/{chan} via {connA}")

            try:
                conn_end = query_connection_end(chain, connA)
            except Exception as e:
                log(f"[warn] failed to query connection end {connA} on {chain}: {e}")
                continue

            client_id_a, client_id_b = extract_client_ids(conn_end)
            if not client_id_a or not client_id_b:
                log(f"[warn] missing client IDs for {chain}/{connA}; skipping")
                continue

            try:
                cstate = query_client_state(chain, client_id_a)
            except Exception as e:
                log(f"[warn] failed to query client state {client_id_a} on {chain}: {e}")
                continue

            subject_chain = extract_chain_id(cstate)
            if not subject_chain:
                log(f"[warn] could not determine counterparty chain-id for {chain}/{connA} (client {client_id_a}); skipping")
                continue

            host_chain = subject_chain
            key = (host_chain, client_id_b)
            if key in seen:
                continue
            seen.add(key)
            triples.append((host_chain, client_id_b, subject_chain))
            log(f"[info] discovered host={host_chain} client={client_id_b} (tracks {subject_chain})")

    return triples


def backfill_client(host_chain: str, client_id: str, end_height: int) -> None:
    try:
        cstate = query_client_state(host_chain, client_id)
    except Exception as e:
        log(f"[error] query client state failed on host={host_chain} client={client_id}: {e}")
        return

    trusted = extract_revision_height(cstate)
    if trusted <= 0:
        log(f"[warn] could not parse trusted height (got {trusted}) on host={host_chain} client={client_id}")

    start = trusted + 1
    if start > end_height:
        log(f"[info] host={host_chain} client={client_id}: already >= end ({trusted} >= {end_height}); skipping")
        return

    log(f"[info] host={host_chain} client={client_id}: backfill {start}..{end_height}")
    failures = 0
    for h in range(start, end_height + 1):
        try:
            update_client(host_chain, client_id, height=h)
        except subprocess.CalledProcessError as e:
            failures += 1
            log(f"[error] update --height {h} failed for host={host_chain} client={client_id}: {e.stderr or e.output}")
            continue
        except Exception as e:
            failures += 1
            log(f"[error] update --height {h} failed for host={host_chain} client={client_id}: {e}")
            continue

    if failures == 0:
        log(f"[ok] host={host_chain} client={client_id}: backfill complete")
    else:
        log(f"[warn] host={host_chain} client={client_id}: backfill complete with {failures} failures")


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Backfill IBC UpdateClient from trusted+1 to migrationHeight+window for all connected channels' counterparties using Hermes."
    )
    ap.add_argument("chains", nargs="+", help="Chain IDs to scan (e.g., gm-a gm-b gm-c)")
    ap.add_argument("--migration-height", type=int, required=True, help="Block height where smoothing starts")
    ap.add_argument("--window", type=int, default=30, help="Smoothing window length (default: 30)")
    ap.add_argument("--end-height", type=int, default=None, help="Override: backfill up to this height instead of migration_height+window")
    args = ap.parse_args()

    end_height = args.end_height if args.end_height is not None else (args.migration_height + args.window)
    if end_height <= 0:
        log("[error] end height must be positive")
        sys.exit(2)

    triples = discover_host_clients(args.chains)
    if not triples:
        log("[warn] no host clients discovered; nothing to do")
        return

    for host_chain, client_id, _subject_chain in triples:
        backfill_client(host_chain, client_id, end_height)

    log("[done] backfill attempted for all discovered host clients")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        log("[warn] interrupted")
        sys.exit(130)

