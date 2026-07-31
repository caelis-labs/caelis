import json
from pathlib import Path
import tempfile
import unittest

from eval.terminalbench.report import build_report


def write_jsonl(trial_dir: Path, *, valid: bool = True, usage: dict[str, int] | None = None) -> None:
    agent_dir = trial_dir / "agent"
    agent_dir.mkdir()
    records = [
        {"schema_version": "caelis.headless/v1", "type": "envelope"},
        {"schema_version": "caelis.headless/v1", "type": "result", "usage": usage or {}},
    ]
    if not valid:
        records.pop()
    (agent_dir / "caelis.jsonl").write_text(
        "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
    )


class ReportTest(unittest.TestCase):
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
                        "exception_info": {"exception_type": "AgentTimeoutError"},
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


if __name__ == "__main__":
    unittest.main()
