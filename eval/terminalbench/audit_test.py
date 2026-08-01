import json
from pathlib import Path
import tempfile
import unittest

from eval.terminalbench.audit import build_audit


class AuditTest(unittest.TestCase):
    def test_complete_run_indexes_root_and_trial_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            run_dir = Path(raw)
            run_id = "preview"
            trial_dir = run_dir / "jobs" / run_id / "fix-git__trial"
            required = [
                run_dir / "tasks.txt",
                run_dir / "dataset-tasks.txt",
                run_dir / "start.log",
                run_dir / "harbor.log",
                run_dir / "score.md",
                run_dir / "bundle" / "cacert.pem",
                run_dir / "bundle" / "caelis-linux-amd64",
                run_dir / "bundle" / "config.json",
                run_dir / "bundle" / "traceaudit",
                run_dir / "bundle" / "verifier-cache.json",
                run_dir / "jobs" / run_id / "config.json",
                run_dir / "jobs" / run_id / "job.log",
                run_dir / "jobs" / run_id / "result.json",
                trial_dir / "config.json",
                trial_dir / "result.json",
                trial_dir / "trial.log",
                trial_dir / "artifacts" / "manifest.json",
                trial_dir / "agent" / "caelis.jsonl",
                trial_dir / "agent" / "caelis.stderr",
                trial_dir / "verifier" / "ctrf.json",
                trial_dir / "verifier" / "reward.txt",
                trial_dir / "verifier" / "test-stdout.txt",
            ]
            for path in required:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("evidence\n", encoding="utf-8")
            (run_dir / "manifest.json").write_text(
                json.dumps({"run_id": run_id, "status": "completed"}), encoding="utf-8"
            )
            (run_dir / "score.json").write_text(
                json.dumps(
                    {
                        "complete": True,
                        "structured_errors": 0,
                        "tasks": [
                            {
                                "task": "terminal-bench/fix-git",
                                "trial_dir": "fix-git__trial",
                                "reward": 1.0,
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )

            audit = build_audit(run_dir)

        self.assertTrue(audit["evidence_complete"])
        self.assertTrue(audit["benchmark_complete"])
        self.assertEqual(audit["missing_required_files"], [])
        self.assertGreater(audit["file_count"], 20)
        self.assertTrue(str(audit["root_digest"]).startswith("sha256:"))


if __name__ == "__main__":
    unittest.main()
