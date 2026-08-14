"""Extract invocation-level token usage from a Caelis headless trace."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any


_USAGE_FIELDS = {
    "n_input_tokens": "prompt_tokens",
    "n_cache_tokens": "cached_input_tokens",
    "n_output_tokens": "completion_tokens",
    "n_reasoning_tokens": "reasoning_tokens",
    "n_total_tokens": "total_tokens",
    "cost_micros": "cost_micros",
}
_MAX_USAGE_FIELDS = {"context_window_tokens": "context_window_tokens"}


def collect_trace_usage(path: Path) -> dict[str, Any]:
    """Sum unique per-invocation usage updates and classify coverage."""
    totals = {field: 0 for field in (*_USAGE_FIELDS, *_MAX_USAGE_FIELDS)}
    populated: set[str] = set()
    seen_usage_events: set[str] = set()
    usage_updates = 0
    terminal_type: str | None = None
    result_usage: dict[str, Any] | None = None

    if not path.is_file():
        return _usage_result(totals, populated, usage_updates, terminal_type)

    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(record, dict):
            continue
        record_type = record.get("type")
        if record_type in {"result", "error"}:
            terminal_type = str(record_type)
            if record_type == "result" and isinstance(record.get("usage"), dict):
                result_usage = record["usage"]
            continue
        if record_type != "envelope" or not isinstance(record.get("envelope"), dict):
            continue
        envelope = record["envelope"]
        update = envelope.get("update")
        if not isinstance(update, dict) or update.get("sessionUpdate") != "usage_update":
            continue
        event_key = envelope.get("event_id")
        if not isinstance(event_key, str) or not event_key:
            event_key = f"line:{line_number}"
        if event_key in seen_usage_events:
            continue
        seen_usage_events.add(event_key)
        usage_updates += 1
        _accumulate(totals, populated, _usage_snapshot(update))

    # Older traces may have only the terminal result usage. It represents the
    # final invocation, so use it only when invocation-level updates are absent.
    if usage_updates == 0 and result_usage is not None:
        _accumulate(totals, populated, result_usage)

    return _usage_result(totals, populated, usage_updates, terminal_type)


def _usage_snapshot(update: dict[str, Any]) -> dict[str, Any]:
    meta = update.get("_meta")
    if isinstance(meta, dict):
        caelis = meta.get("caelis")
        if isinstance(caelis, dict):
            usage = caelis.get("usage")
            if isinstance(usage, dict):
                snapshot = dict(usage)
                sdk = caelis.get("sdk")
                if isinstance(sdk, dict) and isinstance(sdk.get("usage"), dict):
                    snapshot.setdefault("cost_micros", sdk["usage"].get("cost_micros"))
                return snapshot
            sdk = caelis.get("sdk")
            if isinstance(sdk, dict) and isinstance(sdk.get("usage"), dict):
                return sdk["usage"]
    used = _nonnegative_int(update.get("used"))
    return {"total_tokens": used} if used is not None else {}


def _accumulate(
    totals: dict[str, int], populated: set[str], usage: dict[str, Any]
) -> None:
    for target, source in _USAGE_FIELDS.items():
        value = _nonnegative_int(usage.get(source))
        if value is None:
            continue
        totals[target] += value
        populated.add(target)
    for target, source in _MAX_USAGE_FIELDS.items():
        value = _nonnegative_int(usage.get(source))
        if value is None:
            continue
        totals[target] = max(totals[target], value)
        populated.add(target)


def _usage_result(
    totals: dict[str, int],
    populated: set[str],
    usage_updates: int,
    terminal_type: str | None,
) -> dict[str, Any]:
    if populated:
        coverage = "complete" if terminal_type is not None else "partial_lower_bound"
    else:
        coverage = "unavailable"
    return {
        **{
            field: totals[field] if field in populated else None
            for field in (*_USAGE_FIELDS, *_MAX_USAGE_FIELDS)
        },
        "usage_updates": usage_updates,
        "usage_coverage": coverage,
    }


def _nonnegative_int(value: object) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value if value >= 0 else None
    if isinstance(value, str) and value.isdigit():
        return int(value)
    return None
