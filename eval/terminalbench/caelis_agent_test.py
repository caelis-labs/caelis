import json
from pathlib import Path
import tempfile
import unittest

from harbor.models.agent.context import AgentContext

from eval.terminalbench.caelis_agent import CaelisAgent


class CaelisAgentTest(unittest.TestCase):
    def test_command_uses_explicit_yolo_mode_without_sandbox_or_approval_flags(self) -> None:
        agent = object.__new__(CaelisAgent)
        agent.model_name = "provider:xiaomi@token-plan-cn/xiaomi/mimo-v2.5-pro"
        agent._reasoning_effort = "high"

        command = agent._command(
            "repair the task",
            CaelisAgent._REMOTE_STORE / "caelis.jsonl",
            CaelisAgent._REMOTE_STORE / "caelis.stderr",
        )

        self.assertIn("--dangerously-skip-permissions", command)
        self.assertNotIn("-sandbox-backend", command)
        self.assertNotIn("-approval-mode", command)

    def test_populate_context_reads_final_structured_usage(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            logs_dir = Path(raw)
            (logs_dir / "caelis.jsonl").write_text(
                "\n".join(
                    [
                        json.dumps({"schema_version": "caelis.headless/v1", "type": "envelope"}),
                        json.dumps(
                            {
                                "schema_version": "caelis.headless/v1",
                                "type": "result",
                                "usage": {
                                    "prompt_tokens": 100,
                                    "cached_input_tokens": 80,
                                    "completion_tokens": 20,
                                    "reasoning_tokens": 5,
                                    "context_window_tokens": 1000,
                                },
                            }
                        ),
                    ]
                )
                + "\n",
                encoding="utf-8",
            )
            agent = object.__new__(CaelisAgent)
            agent.logs_dir = logs_dir
            agent.model_name = "provider:test/model"
            agent._reasoning_effort = "max"
            context = AgentContext(metadata={"output_contract": "caelis.headless/v1"})
            agent.populate_context_post_run(context)

        self.assertEqual(context.n_input_tokens, 100)
        self.assertEqual(context.n_cache_tokens, 80)
        self.assertEqual(context.n_output_tokens, 20)
        self.assertEqual(context.metadata["reasoning_tokens"], 5)
        self.assertEqual(context.metadata["context_window_tokens"], 1000)
        self.assertEqual(context.metadata["model_profile_id"], "provider:test/model")
        self.assertEqual(context.metadata["reasoning_effort"], "max")

    def test_populate_context_sums_unique_invocation_usage_updates(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            logs_dir = Path(raw)
            usage_update = {
                "schema_version": "caelis.headless/v1",
                "type": "envelope",
                "envelope": {
                    "event_id": "usage-1",
                    "update": {
                        "sessionUpdate": "usage_update",
                        "_meta": {
                            "caelis": {
                                "usage": {
                                    "prompt_tokens": "40",
                                    "cached_input_tokens": "30",
                                    "completion_tokens": "10",
                                    "reasoning_tokens": "3",
                                    "total_tokens": "50",
                                    "context_window_tokens": "1000",
                                }
                            }
                        },
                    },
                },
            }
            records = [
                usage_update,
                usage_update,
                {
                    **usage_update,
                    "envelope": {
                        **usage_update["envelope"],
                        "event_id": "usage-2",
                    },
                },
                {"schema_version": "caelis.headless/v1", "type": "result", "usage": {}},
            ]
            (logs_dir / "caelis.jsonl").write_text(
                "".join(json.dumps(record) + "\n" for record in records), encoding="utf-8"
            )
            agent = object.__new__(CaelisAgent)
            agent.logs_dir = logs_dir
            agent.model_name = "provider:test/model"
            agent._reasoning_effort = "max"
            context = AgentContext(metadata={})
            agent.populate_context_post_run(context)

        self.assertEqual(context.n_input_tokens, 80)
        self.assertEqual(context.n_cache_tokens, 60)
        self.assertEqual(context.n_output_tokens, 20)
        self.assertEqual(context.metadata["reasoning_tokens"], 6)
        self.assertEqual(context.metadata["usage_updates"], 2)
        self.assertEqual(context.metadata["usage_coverage"], "complete")


if __name__ == "__main__":
    unittest.main()
