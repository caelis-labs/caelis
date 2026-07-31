import json
from pathlib import Path
import tempfile
import unittest

from harbor.models.agent.context import AgentContext

from eval.terminalbench.caelis_agent import CaelisAgent


class CaelisAgentTest(unittest.TestCase):
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
            context = AgentContext(metadata={"output_contract": "caelis.headless/v1"})
            agent.populate_context_post_run(context)

        self.assertEqual(context.n_input_tokens, 100)
        self.assertEqual(context.n_cache_tokens, 80)
        self.assertEqual(context.n_output_tokens, 20)
        self.assertEqual(context.metadata["reasoning_tokens"], 5)
        self.assertEqual(context.metadata["context_window_tokens"], 1000)


if __name__ == "__main__":
    unittest.main()
