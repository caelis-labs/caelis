"""Run with python3 -m unittest discover -s scripts -p test_smoke_cleanup.py."""
import contextlib
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

from smoke_cleanup import foreground_host, foreground_store, stop_upgrade_case


@unittest.skipUnless(os.name == 'posix', 'PTY and process group coverage')
class SmokeCleanupTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.case = pathlib.Path(self.tmp.name)
        self.store = self.case / 'store'
        self.store.mkdir()
        (self.store / 'logs').mkdir()
        (self.store / 'logs' / 'service.log').write_text('host log')

    def exercise_upgrade(self, *, keep=False, broken_log=False):
        from host_upgrade_tui import OldTUI
        # The TUI child runs a real idle process. A bad log destination
        # injects an exception into its actual close path after it is reaped.
        binary = self.case / 'tui'
        binary.write_text('#!/bin/sh\nexec /bin/sleep 60\n')
        binary.chmod(0o700)
        tui = OldTUI(binary, self.store, 'test', self.case, os.environ,
                     self.case if broken_log else self.case / 'old-tui.log')
        with subprocess.Popen(['/bin/sleep', '60']) as host:
            def stop():
                host.terminate()
                host.wait(timeout=5)
            try:
                expected = self.assertRaisesRegex(RuntimeError, str(self.store)) if broken_log else contextlib.nullcontext()
                with expected:
                    stop_upgrade_case(self.store, tui, stop, keep)
                self.assertIsNotNone(host.poll())
                with self.assertRaises(ProcessLookupError):
                    os.kill(tui.pid, 0)
            finally:
                if host.poll() is None:
                    stop()
        self.assertEqual(self.store.exists(), keep)
        if not keep:
            self.assertEqual((self.case / 'host-logs' / 'service.log').read_text(), 'host log')

    def test_normal(self):
        self.exercise_upgrade()

    def test_tui_log_failure(self):
        self.exercise_upgrade(broken_log=True)

    def test_keep_stores(self):
        self.exercise_upgrade(keep=True)

    def test_unconfirmed_shutdown_retains_store(self):
        def fail():
            raise TimeoutError('stop timed out')
        with self.assertRaisesRegex(RuntimeError, str(self.store)):
            stop_upgrade_case(self.store, None, fail, False)
        self.assertTrue(self.store.is_dir())

    def test_interrupted_foreground_host(self):
        phase_store = self.case / 'compat-store'
        with open(os.devnull, 'w') as log:
            with self.assertRaises(KeyboardInterrupt):
                with foreground_store(phase_store, False), foreground_host([sys.executable, '-c', 'import time; time.sleep(60)'],
                                     store=phase_store, cwd=self.case, env=os.environ, log=log) as proc:
                    raise KeyboardInterrupt
        self.assertFalse(phase_store.exists())
        self.assertIsNotNone(proc.poll())
        with self.assertRaises(ProcessLookupError):
            os.killpg(proc.pid, 0)
