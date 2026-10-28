"""Cache accounting survives trace projection without inventing missing usage."""

import json
from pathlib import Path
import tempfile
import unittest

from eval.terminalbench.trace_usage import collect_trace_usage


class TraceCacheUsageTest(unittest.TestCase):
    def test_projection_fills_missing_cache_fields_from_same_invocation(self):
        update = {"sessionUpdate": "usage_update", "_meta": {"caelis": {
            "usage": {"prompt_tokens": "100", "cache_write_tokens": "0"},
            "sdk": {"usage": {"cache_write_tokens": "75", "cache_write_5m_tokens": "0", "cached_input_tokens": "6000"}},
        }}}
        record = {"type": "envelope", "envelope": {"event_id": "same-invocation", "update": update}}
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "trace.jsonl"
            path.write_text("\n".join(map(json.dumps, [record, record, {"type": "result"}])))
            usage = collect_trace_usage(path)
        self.assertEqual(usage["usage_updates"], 1)
        self.assertEqual(usage["n_input_tokens"], 100)
        self.assertEqual(usage["n_cache_tokens"], 6000)
        self.assertEqual(usage["n_cache_write_tokens"], 0)
        self.assertEqual(usage["n_cache_write_5m_tokens"], 0)
        self.assertTrue(usage["cache_write_tokens_reported"])
        self.assertIsNone(usage["n_cache_write_1h_tokens"])
        self.assertFalse(usage["cache_write_1h_tokens_reported"])

    def test_terminal_presence_distinguishes_omitted_zero_from_unknown(self):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "trace.jsonl"
            path.write_text(json.dumps({"type": "result", "usage": {
                "cache_write_tokens_reported": True,
                "cache_write_5m_tokens": 0, "cache_write_5m_tokens_reported": False,
            }}))
            usage = collect_trace_usage(path)
        self.assertEqual(usage["n_cache_write_tokens"], 0)
        self.assertTrue(usage["cache_write_tokens_reported"])
        self.assertIsNone(usage["n_cache_write_5m_tokens"])
        self.assertFalse(usage["cache_write_5m_tokens_reported"])
