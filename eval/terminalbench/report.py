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
    if not any(record.get("type") == "envelope" for record in records):
        return "invalid", "missing-envelope"
    final_type = records[-1].get("type")
    if final_type == "result":
        return "complete", None
    if final_type == "error":
        return "error-record", None
    if error_type == "AgentTimeoutError":
        return "truncated-timeout", None
    return "invalid", "missing-final-result"


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
