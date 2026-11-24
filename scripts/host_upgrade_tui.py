import fcntl
import os
import pty
import select
import signal
import struct
import termios
import threading
import time


class OldTUI:
    def __init__(self, binary, store, sid, work, env, log):
        self.output = bytearray()
        self.stop = False
        self.log = log
        self.thread = None
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            try:
                os.chdir(work)
                os.execve(str(binary), [str(binary), '-interactive', '-no-animation',
                                        '-store-dir', str(store), '-session', sid],
                          dict(env, TERM='xterm-256color', COLORTERM='truecolor'))
            finally:
                os._exit(127)
        try:
            fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
            self.thread = threading.Thread(target=self._read, daemon=True)
            self.thread.start()
        except BaseException:
            self.close()
            raise

    def _read(self):
        while not self.stop:
            if not select.select([self.fd], [], [], .1)[0]:
                continue
            try:
                data = os.read(self.fd, 65536)
                if not data:
                    return
                self.output.extend(data)
                if b'\x1b[6n' in data:
                    os.write(self.fd, b'\x1b[1;1R')
                if b'\x1b[c' in data:
                    os.write(self.fd, b'\x1b[?1;2c')
            except OSError:
                return

    def send(self, text):
        os.write(self.fd, text.encode())

    def close(self):
        self.stop = True
        try:
            os.kill(self.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        deadline = time.monotonic() + 3
        try:
            while not os.waitpid(self.pid, os.WNOHANG)[0]:
                if time.monotonic() >= deadline:
                    os.kill(self.pid, signal.SIGKILL)
                    os.waitpid(self.pid, 0)
                    break
                time.sleep(.05)
        finally:
            if self.thread is not None and self.thread.ident is not None:
                self.thread.join(1)
            os.close(self.fd)
            self.log.write_bytes(bytes(self.output))
