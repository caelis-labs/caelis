"""Teardown for processes and private Stores owned by the smoke runners."""
import os
import shutil
import signal
import subprocess
import time
from contextlib import contextmanager


class HostCleanupError(RuntimeError):
    """An owned Host could still be using its Store."""


def handle_termination():
    def interrupt(signum, frame):
        raise KeyboardInterrupt(f'received signal {signum}')

    signal.signal(signal.SIGTERM, interrupt)


def finish_store(store, keep):
    if not keep:
        logs = store / 'logs'
        if logs.is_dir():
            shutil.copytree(logs, store.parent / 'host-logs')
        shutil.rmtree(store)


@contextmanager
def foreground_store(store, keep):
    store.mkdir(mode=0o700)
    try:
        yield
    except HostCleanupError:
        keep = True
        raise
    finally:
        finish_store(store, keep)


def stop_upgrade_case(store, tui, stop_host, keep):
    try:
        if tui is not None:
            try:
                tui.close()
            except Exception as exc:
                raise RuntimeError(f'TUI cleanup failed for {store}: {exc}') from exc
    finally:
        try:
            stop_host()
            finish_store(store, keep)
        except Exception as exc:
            raise RuntimeError(f'Host or Store cleanup failed for {store}: {exc}') from exc


@contextmanager
def foreground_host(command, *, store, cwd, env, log):
    # An isolated process group lets us clean up children even if the Host
    # exits early. Only signal the group created by this invocation.
    grouped = os.name == 'posix'
    process = subprocess.Popen(command, cwd=cwd, env=env, stdout=log,
                               stderr=subprocess.STDOUT, start_new_session=grouped)
    try:
        yield process
    finally:
        try:
            if process.poll() is None:
                if grouped:
                    process.send_signal(signal.SIGINT)
                else:
                    process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            if grouped:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                deadline = time.monotonic() + 5
                while True:
                    try:
                        os.killpg(process.pid, 0)
                    except ProcessLookupError:
                        break
                    if time.monotonic() >= deadline:
                        raise TimeoutError('Host process group still exists')
                    time.sleep(.05)
        except Exception as exc:
            raise HostCleanupError(f'Host cleanup failed for {store}: {exc}') from exc
