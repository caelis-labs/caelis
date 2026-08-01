import json
from pathlib import Path
import tempfile
import unittest

from eval.terminalbench.report import build_report, cache_summary
from eval.terminalbench.trace_usage import collect_trace_usage


def envelope_record() -> dict[str, object]:
    turn = {"handle_id": "handle-1", "run_id": "run-1", "turn_id": "turn-1"}
    return {
        "schema_version": "caelis.headless/v1",
        "type": "envelope",
        "session_id": "session-1",
        "turn": turn,
        "envelope": {
            "kind": "caelis/notice",
            "session_id": "session-1",
            **turn,
        },
    }


def result_record(*, usage: dict[str, int] | None = None) -> dict[str, object]:
    return {
        "schema_version": "caelis.headless/v1",
        "type": "result",
        "session_id": "session-1",
        "turn": {"handle_id": "handle-1", "run_id": "run-1", "turn_id": "turn-1"},
        "status": "completed",
        "output": "done",
        "usage": usage or {},
    }


def write_jsonl(trial_dir: Path, *, valid: bool = True, usage: dict[str, int] | None = None) -> None:
    agent_dir = trial_dir / "agent"
    agent_dir.mkdir()
    records = [envelope_record(), result_record(usage=usage)]
    if not valid:
        records.pop()
    (agent_dir / "caelis.jsonl").write_text(
        "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
    )


class ReportTest(unittest.TestCase):
    def test_cache_metadata_is_preserved_and_summarized(self) -> None:
        cache = {
            "mode": "apt-cacher-ng+uv-mirror",
            "apt": {"image_reused": True, "volume_reused": False},
            "uv": {"image_reused": True},
        }
        report = build_report(
            {"run_id": "test", "verifier_cache": cache},
            [],
            Path("missing"),
        )
        self.assertEqual(report["verifier_cache"], cache)
        self.assertEqual(
            cache_summary(report["verifier_cache"]),
            "apt-cacher-ng+uv-mirror; apt_image_reused=true; "
            "apt_volume_reused=false; uv_image_reused=true",
        )

    def test_errors_and_missing_trials_score_as_zero(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            passed = job_dir / "passed"
            errored = job_dir / "errored"
            passed.mkdir()
            errored.mkdir()
            write_jsonl(passed)
            write_jsonl(errored, valid=False)
            (passed / "result.json").write_text(
                json.dumps(
                    {
                        "task_name": "fix-git",
                        "verifier_result": {"rewards": {"reward": 1}},
                    }
                ),
                encoding="utf-8",
            )
            (errored / "result.json").write_text(
                json.dumps(
                    {
                        "task_name": "regex-log",
                        "exception_info": {"exception_type": "NonZeroAgentExitCodeError"},
                    }
                ),
                encoding="utf-8",
            )
            report = build_report(
                {"run_id": "test"},
                ["terminal-bench/fix-git", "terminal-bench/regex-log", "terminal-bench/missing"],
                job_dir,
            )
        self.assertEqual(report["planned"], 3)
        self.assertEqual(report["completed"], 2)
        self.assertEqual(report["passed"], 1)
        self.assertEqual(report["errors"], 1)
        self.assertEqual(report["structured_errors"], 1)
        self.assertAlmostEqual(report["score"], 1 / 3)
        self.assertFalse(report["complete"])

    def test_complete_report_includes_agent_usage(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "passed"
            trial.mkdir()
            write_jsonl(
                trial,
                usage={
                    "prompt_tokens": 100,
                    "cached_input_tokens": 80,
                    "completion_tokens": 20,
                    "reasoning_tokens": 5,
                },
            )
            (trial / "result.json").write_text(
                json.dumps(
                    {
                        "task_name": "fix-git",
                        "verifier_result": {"rewards": {"reward": 1}},
                    }
                ),
                encoding="utf-8",
            )
            report = build_report({"run_id": "test"}, ["terminal-bench/fix-git"], job_dir)
        self.assertTrue(report["complete"])
        self.assertEqual(report["structured_errors"], 0)
        self.assertEqual(report["n_input_tokens"], 100)
        self.assertEqual(report["n_cache_tokens"], 80)
        self.assertEqual(report["n_output_tokens"], 20)
        self.assertEqual(report["n_reasoning_tokens"], 5)

    def test_usage_updates_are_summed_and_timeout_is_lower_bound(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            output = Path(raw) / "caelis.jsonl"
            records = []
            for index, prompt_tokens in enumerate((100, 120), 1):
                records.append(
                    {
                        "schema_version": "caelis.headless/v1",
                        "type": "envelope",
                        "envelope": {
                            "event_id": f"usage-{index}",
                            "update": {
                                "sessionUpdate": "usage_update",
                                "_meta": {
                                    "caelis": {
                                        "usage": {
                                            "prompt_tokens": str(prompt_tokens),
                                            "cached_input_tokens": "80",
                                            "completion_tokens": "20",
                                            "reasoning_tokens": "5",
                                            "total_tokens": str(prompt_tokens + 20),
                                        }
                                    }
                                },
                            },
                        },
                    }
                )
            output.write_text(
                "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
            )
            usage = collect_trace_usage(output)
        self.assertEqual(usage["n_input_tokens"], 220)
        self.assertEqual(usage["n_cache_tokens"], 160)
        self.assertEqual(usage["n_output_tokens"], 40)
        self.assertEqual(usage["n_reasoning_tokens"], 10)
        self.assertEqual(usage["n_total_tokens"], 260)
        self.assertEqual(usage["usage_updates"], 2)
        self.assertEqual(usage["usage_coverage"], "partial_lower_bound")

    def test_timeout_with_valid_jsonl_prefix_is_classified_not_corrupted(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "timeout"
            trial.mkdir()
            write_jsonl(trial, valid=False)
            (trial / "result.json").write_text(
                json.dumps(
                    {
                        "task_name": "headless-terminal",
                        "exception_info": {"exception_type": "AgentTimeoutError"},
                        "verifier_result": {"rewards": {"reward": 0}},
                    }
                ),
                encoding="utf-8",
            )
            report = build_report(
                {"run_id": "test"}, ["terminal-bench/headless-terminal"], job_dir
            )
        self.assertTrue(report["complete"])
        self.assertEqual(report["structured_errors"], 0)
        self.assertEqual(report["tasks"][0]["structured_output_state"], "truncated-timeout")

    def test_structured_error_record_is_a_valid_failure_terminal(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "failed"
            trial.mkdir()
            agent_dir = trial / "agent"
            agent_dir.mkdir()
            (agent_dir / "caelis.jsonl").write_text(
                "\n".join(
                    json.dumps(record)
                    for record in [
                        {
                            "schema_version": "caelis.headless/v1",
                            "type": "error",
                            "message": "agent failed before a Session was created",
                        },
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            (trial / "result.json").write_text(
                json.dumps(
                    {
                        "task_name": "regex-log",
                        "exception_info": {"exception_type": "NonZeroAgentExitCodeError"},
                        "verifier_result": {"rewards": {"reward": 0}},
                    }
                ),
                encoding="utf-8",
            )
            report = build_report({"run_id": "test"}, ["terminal-bench/regex-log"], job_dir)
        self.assertTrue(report["complete"])
        self.assertEqual(report["structured_errors"], 0)
        self.assertEqual(report["tasks"][0]["structured_output_state"], "error-record")

    def test_terminal_followed_by_an_envelope_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "bad-order"
            trial.mkdir()
            agent_dir = trial / "agent"
            agent_dir.mkdir()
            records = [envelope_record(), result_record(), envelope_record()]
            (agent_dir / "caelis.jsonl").write_text(
                "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
            )
            (trial / "result.json").write_text(
                json.dumps({"task_name": "fix-git", "verifier_result": {"rewards": {"reward": 1}}}),
                encoding="utf-8",
            )
            report = build_report({"run_id": "test"}, ["terminal-bench/fix-git"], job_dir)
        self.assertFalse(report["complete"])
        self.assertEqual(
            report["tasks"][0]["structured_output_error"], "record-after-terminal-line-3"
        )

    def test_mismatched_envelope_target_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "bad-target"
            trial.mkdir()
            agent_dir = trial / "agent"
            agent_dir.mkdir()
            envelope = envelope_record()
            envelope["envelope"]["turn_id"] = "other-turn"  # type: ignore[index]
            records = [envelope, result_record()]
            (agent_dir / "caelis.jsonl").write_text(
                "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
            )
            (trial / "result.json").write_text(
                json.dumps({"task_name": "fix-git", "verifier_result": {"rewards": {"reward": 1}}}),
                encoding="utf-8",
            )
            report = build_report({"run_id": "test"}, ["terminal-bench/fix-git"], job_dir)
        self.assertFalse(report["complete"])
        self.assertEqual(
            report["tasks"][0]["structured_output_error"],
            "envelope-turn_id-mismatch-line-1",
        )

    def test_non_terminal_result_status_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "running-result"
            trial.mkdir()
            agent_dir = trial / "agent"
            agent_dir.mkdir()
            result = result_record()
            result["status"] = "running"
            records = [envelope_record(), result]
            (agent_dir / "caelis.jsonl").write_text(
                "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
            )
            (trial / "result.json").write_text(
                json.dumps({"task_name": "fix-git", "verifier_result": {"rewards": {"reward": 1}}}),
                encoding="utf-8",
            )
            report = build_report({"run_id": "test"}, ["terminal-bench/fix-git"], job_dir)
        self.assertFalse(report["complete"])
        self.assertEqual(
            report["tasks"][0]["structured_output_error"], "invalid-result-status-line-2"
        )

    def test_result_target_must_match_envelope_target(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            job_dir = Path(raw)
            trial = job_dir / "bad-result-target"
            trial.mkdir()
            agent_dir = trial / "agent"
            agent_dir.mkdir()
            result = result_record()
            result["turn"]["turn_id"] = "other-turn"  # type: ignore[index]
            records = [envelope_record(), result]
            (agent_dir / "caelis.jsonl").write_text(
                "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
            )
            (trial / "result.json").write_text(
                json.dumps({"task_name": "fix-git", "verifier_result": {"rewards": {"reward": 1}}}),
                encoding="utf-8",
            )
            report = build_report({"run_id": "test"}, ["terminal-bench/fix-git"], job_dir)
        self.assertFalse(report["complete"])
        self.assertEqual(
            report["tasks"][0]["structured_output_error"], "record-target-mismatch-line-2"
        )


if __name__ == "__main__":
    unittest.main()
