#!/usr/bin/env python3
"""Build stable JSON and Markdown score reports from one Harbor job."""

from __future__ import annotations

import argparse
from datetime import datetime
import json
from pathlib import Path
from typing import Any


def load_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"expected JSON object: {path}")
    return value


def primary_reward(result: dict[str, Any]) -> float | None:
    verifier = result.get("verifier_result")
    if not isinstance(verifier, dict):
        return None
    rewards = verifier.get("rewards")
    if not isinstance(rewards, dict) or not rewards:
        return None
    raw = rewards.get("reward", next(iter(rewards.values())))
    if isinstance(raw, (int, float)):
        return float(raw)
    return None


def normalize_task_name(name: str) -> str:
    name = name.strip()
    if name.startswith("terminal-bench/"):
        return name
    return f"terminal-bench/{name}"


def collect_trials(job_dir: Path) -> dict[str, tuple[dict[str, Any], Path]]:
    trials: dict[str, tuple[dict[str, Any], Path]] = {}
    if not job_dir.is_dir():
        return trials
    for result_path in sorted(job_dir.glob("*/result.json")):
        result = load_json(result_path)
        task_name = normalize_task_name(str(result.get("task_name", "")))
        if task_name != "terminal-bench/":
            trials[task_name] = (result, result_path.parent)
    return trials


def duration_seconds(result: dict[str, Any]) -> float | None:
    started = result.get("started_at")
    finished = result.get("finished_at")
    if not isinstance(started, str) or not isinstance(finished, str):
        return None
    try:
        return (datetime.fromisoformat(finished) - datetime.fromisoformat(started)).total_seconds()
    except ValueError:
        return None


def usage_totals(trials: dict[str, tuple[dict[str, Any], Path]]) -> dict[str, int | None]:
    fields = ("n_input_tokens", "n_cache_tokens", "n_output_tokens", "n_reasoning_tokens")
    totals: dict[str, int | None] = {field: None for field in fields}
    for result, trial_dir in trials.values():
        agent_result = result.get("agent_result")
        structured_usage = caelis_structured_usage(trial_dir)
        for field in fields:
            value = agent_result.get(field) if isinstance(agent_result, dict) else None
            if not isinstance(value, int) or isinstance(value, bool):
                value = structured_usage.get(field)
            if isinstance(value, int) and not isinstance(value, bool):
                totals[field] = (totals[field] or 0) + value
    return totals


def caelis_structured_usage(trial_dir: Path) -> dict[str, int]:
    output_path = trial_dir / "agent" / "caelis.jsonl"
    if not output_path.is_file():
        return {}
    for line in reversed(output_path.read_text(encoding="utf-8").splitlines()):
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(record, dict) or record.get("type") != "result":
            continue
        usage = record.get("usage")
        if not isinstance(usage, dict):
            return {}
        mapping = {
            "n_input_tokens": "prompt_tokens",
            "n_cache_tokens": "cached_input_tokens",
            "n_output_tokens": "completion_tokens",
            "n_reasoning_tokens": "reasoning_tokens",
        }
        return {
            target: usage[source]
            for target, source in mapping.items()
            if isinstance(usage.get(source), int) and not isinstance(usage[source], bool)
        }
    return {}


def structured_output_check(trial_dir: Path, error_type: str | None) -> tuple[str, str | None]:
    output_path = trial_dir / "agent" / "caelis.jsonl"
    if not output_path.is_file():
        return "invalid", "missing-caelis-jsonl"
    records: list[dict[str, Any]] = []
    for line_number, line in enumerate(output_path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            return "invalid", f"invalid-json-line-{line_number}"
        if not isinstance(record, dict):
            return "invalid", f"non-object-line-{line_number}"
        if record.get("schema_version") != "caelis.headless/v1":
            return "invalid", f"invalid-schema-line-{line_number}"
        if record.get("type") not in {"envelope", "result", "error"}:
            return "invalid", f"invalid-record-type-line-{line_number}"
        records.append(record)
    if not records:
        return "invalid", "empty-caelis-jsonl"

    terminal_index: int | None = None
    envelope_count = 0
    expected_target: tuple[str, str, str, str] | None = None
    for index, record in enumerate(records):
        record_type = record["type"]
        line_number = index + 1
        if terminal_index is not None:
            return "invalid", f"record-after-terminal-line-{line_number}"
        if record_type == "envelope":
            envelope_count += 1
            error = validate_envelope_record(record)
        elif record_type == "result":
            terminal_index = index
            error = validate_result_record(record)
        else:
            terminal_index = index
            error = validate_error_record(record)
        if error is not None:
            return "invalid", f"{error}-line-{line_number}"
        if record_type in {"envelope", "result"}:
            target = record_target(record)
            if expected_target is None:
                expected_target = target
            elif target != expected_target:
                return "invalid", f"record-target-mismatch-line-{line_number}"

    if terminal_index is not None:
        final_type = records[terminal_index]["type"]
        if final_type == "result":
            if envelope_count == 0:
                return "invalid", "result-without-envelope"
            return "complete", None
        return "error-record", None
    if error_type == "AgentTimeoutError" and envelope_count > 0:
        return "truncated-timeout", None
    return "invalid", "missing-final-result"


def nonempty_string(value: Any) -> bool:
    return isinstance(value, str) and bool(value.strip())


def validate_turn(record: dict[str, Any]) -> str | None:
    turn = record.get("turn")
    if not isinstance(turn, dict):
        return "invalid-turn"
    for field in ("handle_id", "run_id", "turn_id"):
        if not nonempty_string(turn.get(field)):
            return f"invalid-turn-{field}"
    return None


def record_target(record: dict[str, Any]) -> tuple[str, str, str, str]:
    turn = record["turn"]
    return record["session_id"], turn["handle_id"], turn["run_id"], turn["turn_id"]


def validate_envelope_record(record: dict[str, Any]) -> str | None:
    if not nonempty_string(record.get("session_id")):
        return "invalid-session-id"
    if error := validate_turn(record):
        return error
    envelope = record.get("envelope")
    if not isinstance(envelope, dict):
        return "invalid-envelope"
    turn = record["turn"]
    expected = {
        "session_id": record["session_id"],
        "handle_id": turn["handle_id"],
        "run_id": turn["run_id"],
        "turn_id": turn["turn_id"],
    }
    for field, value in expected.items():
        if envelope.get(field) != value:
            return f"envelope-{field}-mismatch"
    if not nonempty_string(envelope.get("kind")):
        return "invalid-envelope-kind"
    return None


def validate_result_record(record: dict[str, Any]) -> str | None:
    if not nonempty_string(record.get("session_id")):
        return "invalid-session-id"
    if error := validate_turn(record):
        return error
    if record.get("status") not in {
        "completed",
        "failed",
        "interrupted",
        "cancelled",
        "canceled",
        "terminated",
        "unknown_outcome",
    }:
        return "invalid-result-status"
    usage = record.get("usage")
    if usage is not None and not isinstance(usage, dict):
        return "invalid-result-usage"
    return None


def validate_error_record(record: dict[str, Any]) -> str | None:
    if not nonempty_string(record.get("message")):
        return "invalid-error-message"
    session_id = record.get("session_id")
    if session_id is not None and not nonempty_string(session_id):
        return "invalid-session-id"
    return None


def build_report(manifest: dict[str, Any], expected: list[str], job_dir: Path) -> dict[str, Any]:
    trials = collect_trials(job_dir)
    rows: list[dict[str, Any]] = []
    reward_sum = 0.0
    scored = 0
    passed = 0
    errors = 0
    structured_errors = 0
    for task_name in expected:
        normalized = normalize_task_name(task_name)
        trial = trials.get(normalized)
        result = trial[0] if trial is not None else None
        reward = primary_reward(result) if result is not None else None
        exception = result.get("exception_info") if result is not None else None
        error_type = exception.get("exception_type") if isinstance(exception, dict) else None
        output_state, output_error = (
            structured_output_check(trial[1], error_type) if trial is not None else ("missing", None)
        )
        if reward is not None:
            scored += 1
            reward_sum += reward
            if reward >= 1.0:
                passed += 1
        if error_type:
            errors += 1
        if output_error:
            structured_errors += 1
        rows.append(
            {
                "task": normalized,
                "reward": reward,
                "passed": reward is not None and reward >= 1.0,
                "error": error_type,
                "duration_seconds": duration_seconds(result) if result is not None else None,
                "structured_output": output_error is None and trial is not None,
                "structured_output_state": output_state,
                "structured_output_error": output_error,
            }
        )
    planned = len(expected)
    report = {
        "schema_version": "caelis.eval.terminalbench.report/v1",
        "run_id": manifest.get("run_id"),
        "commit": manifest.get("commit"),
        "dataset": manifest.get("dataset"),
        "dataset_digest": manifest.get("dataset_digest"),
        "taskset": manifest.get("taskset"),
        "model_profile_id": manifest.get("model_profile_id"),
        "planned": planned,
        "completed": len(trials),
        "scored": scored,
        "passed": passed,
        "errors": errors,
        "structured_errors": structured_errors,
        "score": reward_sum / planned if planned else 0.0,
        "mean_scored_reward": reward_sum / scored if scored else None,
        "complete": len(trials) == planned and scored == planned and structured_errors == 0,
        "tasks": rows,
    }
    report.update(usage_totals(trials))
    return report


def markdown(report: dict[str, Any]) -> str:
    lines = [
        "# Caelis Terminal-Bench 2.1 report",
        "",
        f"- Run: `{report['run_id']}`",
        f"- Commit: `{report['commit']}`",
        f"- Task set: `{report['taskset']}`",
        f"- Score: **{report['score']:.3f}** ({report['passed']}/{report['planned']} passed)",
        f"- Completed/scored/errors: {report['completed']}/{report['scored']}/{report['errors']}",
        f"- Structured output errors: {report['structured_errors']}",
        f"- Tokens input/cache/output/reasoning: {report['n_input_tokens']}/{report['n_cache_tokens']}/{report['n_output_tokens']}/{report['n_reasoning_tokens']}",
        f"- Complete: `{str(report['complete']).lower()}`",
        "",
        "| Task | Reward | Error | JSONL | Seconds |",
        "| --- | ---: | --- | --- | ---: |",
    ]
    for row in report["tasks"]:
        reward = "-" if row["reward"] is None else f"{row['reward']:.3f}"
        error = row["error"] or "-"
        structured = row["structured_output_error"] or row["structured_output_state"]
        seconds = "-" if row["duration_seconds"] is None else f"{row['duration_seconds']:.1f}"
        lines.append(f"| `{row['task']}` | {reward} | {error} | {structured} | {seconds} |")
    lines.append("")
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--job-dir", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--tasks", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()

    manifest = load_json(args.manifest)
    expected = [line.strip() for line in args.tasks.read_text(encoding="utf-8").splitlines() if line.strip()]
    report = build_report(manifest, expected, args.job_dir)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    (args.output_dir / "score.json").write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    (args.output_dir / "score.md").write_text(markdown(report), encoding="utf-8")
    print(markdown(report), end="")
    return 0 if report["complete"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
