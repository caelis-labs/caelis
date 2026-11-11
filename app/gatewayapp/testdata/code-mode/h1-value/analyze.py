#!/usr/bin/env python3
"""Analyze frozen raw evidence; never invokes a model or replays a tool."""
import argparse
import collections
import json
from pathlib import Path


def load(path):
    return json.loads(path.read_text())


def provider_usage(path):
    if not path.exists():
        return None
    found = None
    for line in path.read_text(errors="replace").splitlines():
        if line.startswith("data: "):
            try:
                data = json.loads(line[6:])
            except ValueError:
                continue
            response = data.get("response", {})
            if data.get("type") == "response.completed" and isinstance(response.get("usage"), dict):
                found = response["usage"]
    if found is None:
        return None
    prompt, output = found.get("input_tokens"), found.get("output_tokens")
    cached = found.get("input_tokens_details", {}).get("cached_tokens")
    fresh = prompt - cached if isinstance(prompt, int) and isinstance(cached, int) and 0 <= cached <= prompt else None
    return {"prompt": prompt, "cached": cached, "fresh": fresh, "output": output, "raw": found}


def analyze(root):
    manifest = load(root / "manifest.json")
    sessions = []
    for plan in manifest["schedule"]:
        folder = root / plan["id"]
        turns = []
        for turn in (1, 2):
            generations = []
            for meta_file in sorted(folder.glob("generation-*.meta.json")):
                meta = load(meta_file)
                if meta["turn"] != turn:
                    continue
                stem = meta_file.name.replace(".meta.json", "")
                wires = [p for p in folder.glob("wire-*.meta.json") if load(p).get("generation") == meta["generation"]]
                usage = provider_usage(folder / wires[0].name.replace(".meta.json", ".response.raw")) if len(wires) == 1 else None
                receipt_file = folder / (stem + ".receipt.json")
                receipt = load(receipt_file) if receipt_file.exists() else None
                generations.append({"generation": meta["generation"], "kind": meta["kind"], "provider_usage": usage, "invocation": receipt, "wire_requests": len(wires), "prefix_bytes": meta["prefix_bytes"], "prefix_sha256": meta["prefix_sha256"], "prefix_tokens": None})
            sums = {}
            for field in ("prompt", "cached", "fresh", "output"):
                values = [(g["provider_usage"] or {}).get(field) for g in generations]
                sums[field] = sum(values) if values and all(isinstance(v, int) for v in values) else None
            kinds = collections.Counter(g["kind"] for g in generations)
            counts = {"main": kinds["main"], "selector": kinds["selector"], "total": len(generations), "wire_total": sum(g["wire_requests"] for g in generations)}
            assertions_file = folder / f"turn-{turn}.assertions.json"
            assertions = load(assertions_file) if assertions_file.exists() else None
            receipts_file = folder / f"turn-{turn}.receipts.json"
            receipts = load(receipts_file) if receipts_file.exists() else {}
            contexts = [load(p) for p in sorted(folder.glob("context-*.json")) if load(p).get("turn") == turn]
            if plan["task"] == "small":
                business = bool(assertions and all(assertions.get(k) for k in ("schema", "aggregate", "both_inputs_read", "context7_query")))
            else:
                business = bool(assertions and assertions.get("exact_oracle") and assertions.get("all_batches_actually_read"))
            turns.append({"turn": turn, "business": business, "assertions": assertions, "round_trips": counts, "usage": sums, "generations": generations, "tool_receipts": receipts, "context_projections": contexts, "api_evidence": "manual original Context7 result review required; unavailable is not supported API" if plan["task"] == "small" else "synthetic inputs / real local execution"})
        sessions.append({**plan, "turns": turns})
    comparisons = []
    for task in ("small", "filter"):
        native = next(s for s in sessions if s["task"] == task and s["route"] == "native")
        js = next(s for s in sessions if s["task"] == task and s["route"] == "js")
        per_turn = []
        for n, j in zip(native["turns"], js["turns"]):
            delta, targets = {}, {}
            for name in ("main", "selector", "total"):
                nv, jv = n["round_trips"][name], j["round_trips"][name]
                delta[name] = {"native": nv, "js": jv, "absolute": jv - nv, "percent": 100 * (jv - nv) / nv if nv else None}
            for name in ("prompt", "cached", "fresh", "output"):
                nv, jv = n["usage"][name], j["usage"][name]
                delta[name] = {"native": nv, "js": jv, "absolute": jv - nv if nv is not None and jv is not None else None, "percent": 100 * (jv - nv) / nv if nv not in (None, 0) and jv is not None else None}
            complete = bool(n["generations"] and j["generations"] and all(g["wire_requests"] == 1 and g["invocation"] and g["invocation"].get("Outcome") == "completed" for t in (n, j) for g in t["generations"]))
            for name in ("main", "total", "fresh", "output"):
                value = delta[name]["absolute"]
                targets[name] = (value < 0 if task == "filter" and name != "output" else value <= 0) if value is not None and complete else None
            per_turn.append({"turn": n["turn"], "both_business": n["business"] and j["business"], "complete_invocations": complete, "deltas": delta, "targets": targets})
        pair_totals = {}
        for name in ("main", "selector", "total", "prompt", "cached", "fresh", "output"):
            values = [t["deltas"][name] for t in per_turn]
            nv = sum(v["native"] for v in values) if all(v["native"] is not None for v in values) else None
            jv = sum(v["js"] for v in values) if all(v["js"] is not None for v in values) else None
            pair_totals[name] = {"native": nv, "js": jv, "absolute": jv - nv if nv is not None and jv is not None else None, "percent": 100 * (jv - nv) / nv if nv not in (None, 0) and jv is not None else None}

        pair_targets = {}
        complete_pair = all(t["complete_invocations"] for t in per_turn)
        for name in ("main", "total", "fresh", "output"):
            value = pair_totals[name]["absolute"]
            pair_targets[name] = (value < 0 if task == "filter" and name != "output" else value <= 0) if value is not None and complete_pair else None
        comparisons.append({"task": task, "one_pair_only": True, "per_turn": per_turn, "pair_totals": pair_totals, "pair_targets": pair_targets, "benefit_targets_pass": all(t["both_business"] and t["complete_invocations"] and all(v is True for v in t["targets"].values()) for t in per_turn) and all(v is True for v in pair_targets.values())})
    return {"classification": "provider actual usage only; no token estimate or cold-cache assumption; tasks are not pooled", "sessions": sessions, "comparisons": comparisons, "prefix_token_components": "unavailable unless separately present in provider evidence", "fresh_semantics": "derived input_tokens minus cached_tokens only when both original provider fields exist; not an independent provider component", "context_bytes": "actual SDK projection JSON bytes; not tokens or causal per-result token attribution"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    args = parser.parse_args()
    output = analyze(args.root)
    (args.root / "analysis.json").write_text(json.dumps(output, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"tasks": [{"task": c["task"], "benefit_targets_pass": c["benefit_targets_pass"]} for c in output["comparisons"]]}))
