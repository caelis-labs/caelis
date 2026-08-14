#!/usr/bin/env python3
"""Create a content-addressed evidence index for one Terminal-Bench run."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
from typing import Any


_OUTPUT_FILES = {"audit.json", "audit.md"}


def load_json(path: Path) -> dict[str, Any]:
    with path.open(encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"expected JSON object: {path}")
    return value


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def file_inventory(run_dir: Path) -> list[dict[str, Any]]:
    files: list[dict[str, Any]] = []
    for path in sorted(run_dir.rglob("*")):
        if not path.is_file() or path.name in _OUTPUT_FILES or path.name.endswith(".next.json"):
            continue
        files.append(
            {
                "path": str(path.relative_to(run_dir)),
                "bytes": path.stat().st_size,
                "sha256": sha256(path),
            }
        )
    return files


def evidence_paths(score: dict[str, Any], run_id: str) -> tuple[list[str], list[dict[str, Any]]]:
    required = [
        "manifest.json",
        "tasks.txt",
        "dataset-tasks.txt",
        "start.log",
        "harbor.log",
        "score.json",
        "score.md",
        "bundle/cacert.pem",
        "bundle/caelis-linux-amd64",
        "bundle/config.json",
        "bundle/traceaudit",
        "bundle/verifier-cache.json",
        f"jobs/{run_id}/config.json",
        f"jobs/{run_id}/job.log",
        f"jobs/{run_id}/result.json",
    ]
    tasks: list[dict[str, Any]] = []
    for row in score.get("tasks", []):
        if not isinstance(row, dict):
            continue
        trial_dir = row.get("trial_dir")
        entry = {
            "task": row.get("task"),
            "trial_dir": trial_dir,
            "reward": row.get("reward"),
            "error": row.get("error"),
            "structured_output_state": row.get("structured_output_state"),
            "structured_output_error": row.get("structured_output_error"),
            "usage": row.get("usage"),
            "phase_seconds": row.get("phase_seconds"),
        }
        tasks.append(entry)
        if not isinstance(trial_dir, str) or not trial_dir:
            continue
        prefix = f"jobs/{run_id}/{trial_dir}"
        required.extend(
            [
                f"{prefix}/config.json",
                f"{prefix}/result.json",
                f"{prefix}/trial.log",
                f"{prefix}/artifacts/manifest.json",
                f"{prefix}/agent/caelis.jsonl",
                f"{prefix}/agent/caelis.stderr",
            ]
        )
        if row.get("reward") is not None:
            required.extend(
                [
                    f"{prefix}/verifier/ctrf.json",
                    f"{prefix}/verifier/reward.txt",
                    f"{prefix}/verifier/test-stdout.txt",
                ]
            )
    return required, tasks


def build_audit(run_dir: Path) -> dict[str, Any]:
    manifest = load_json(run_dir / "manifest.json")
    score = load_json(run_dir / "score.json")
    run_id = str(manifest.get("run_id", ""))
    files = file_inventory(run_dir)
    indexed_paths = {item["path"] for item in files}
    required, tasks = evidence_paths(score, run_id)
    missing = sorted(set(required) - indexed_paths)
    digest = hashlib.sha256()
    for item in files:
        digest.update(
            f"{item['sha256']}  {item['bytes']}  {item['path']}\n".encode("utf-8")
        )
    evidence_complete = not missing and score.get("structured_errors") == 0
    return {
        "schema_version": "caelis.eval.terminalbench.audit/v1",
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "run_id": run_id,
        "evidence_complete": evidence_complete,
        "benchmark_complete": bool(score.get("complete")),
        "run_status": manifest.get("status"),
        "root_digest": f"sha256:{digest.hexdigest()}",
        "file_count": len(files),
        "total_bytes": sum(item["bytes"] for item in files),
        "missing_required_files": missing,
        "summary": {
            "commit": manifest.get("commit"),
            "source_clean": manifest.get("source_clean"),
            "model_profile_id": manifest.get("model_profile_id"),
            "model": manifest.get("model"),
            "reasoning_effort": manifest.get("reasoning_effort"),
            "execution_mode": manifest.get("execution_mode"),
            "started_at": manifest.get("started_at"),
            "finished_at": manifest.get("finished_at"),
            "duration_seconds": score.get("duration_seconds"),
            "planned": score.get("planned"),
            "scored": score.get("scored"),
            "passed": score.get("passed"),
            "score": score.get("score"),
            "n_input_tokens": score.get("n_input_tokens"),
            "n_cache_tokens": score.get("n_cache_tokens"),
            "n_output_tokens": score.get("n_output_tokens"),
            "n_reasoning_tokens": score.get("n_reasoning_tokens"),
            "n_total_tokens": score.get("n_total_tokens"),
            "usage_coverage": score.get("usage_coverage"),
            "phase_seconds": score.get("phase_seconds"),
        },
        "tasks": tasks,
        "files": files,
    }


def markdown(audit: dict[str, Any]) -> str:
    summary = audit["summary"]
    missing = audit["missing_required_files"]
    lines = [
        "# Caelis Terminal-Bench audit",
        "",
        f"- Run: `{audit['run_id']}`",
        f"- Evidence complete: `{str(audit['evidence_complete']).lower()}`",
        f"- Benchmark complete: `{str(audit['benchmark_complete']).lower()}`",
        f"- Root digest: `{audit['root_digest']}`",
        f"- Files/bytes: {audit['file_count']}/{audit['total_bytes']}",
        f"- Model/effort: `{summary.get('model')}` / `{summary.get('reasoning_effort')}`",
        f"- Score: `{summary.get('score')}` ({summary.get('passed')}/{summary.get('planned')})",
        f"- Tokens input/cache/output/reasoning/total: {summary.get('n_input_tokens')}/{summary.get('n_cache_tokens')}/{summary.get('n_output_tokens')}/{summary.get('n_reasoning_tokens')}/{summary.get('n_total_tokens')}",
        f"- Missing required files: {len(missing)}",
        "",
    ]
    if missing:
        lines.extend(["## Missing evidence", ""])
        lines.extend(f"- `{path}`" for path in missing)
        lines.append("")
    lines.extend(
        [
            "The complete content-addressed file inventory and per-task evidence map are in `audit.json`.",
            "",
        ]
    )
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--run-dir", type=Path, required=True)
    args = parser.parse_args()
    audit = build_audit(args.run_dir)
    (args.run_dir / "audit.json").write_text(
        json.dumps(audit, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    (args.run_dir / "audit.md").write_text(markdown(audit), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
