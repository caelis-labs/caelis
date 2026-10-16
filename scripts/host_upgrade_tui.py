import os, pty, fcntl, termios, struct, threading, select, time, signal
class OldTUI:
 def __init__(self,binary,store,sid,work,env,log):
  self.output=bytearray();self.stop=False;self.log=log
  pid,fd=pty.fork()
  if pid==0:
   os.chdir(work);os.execve(str(binary),[str(binary),'-interactive','-no-animation','-store-dir',str(store),'-session',sid],dict(env,TERM='xterm-256color',COLORTERM='truecolor'))
  self.pid,self.fd=pid,fd
  fcntl.ioctl(fd,termios.TIOCSWINSZ,struct.pack('HHHH',40,120,0,0))
  def read():
   while not self.stop:
    if not select.select([fd],[],[],.1)[0]:continue
    try:b=os.read(fd,65536)
    except OSError:return
    self.output.extend(b)
    if b'\x1b[6n' in b:os.write(fd,b'\x1b[1;1R')
    if b'\x1b[c' in b:os.write(fd,b'\x1b[?1;2c')
  self.thread=threading.Thread(target=read,daemon=True);self.thread.start()
 def send(self,text):os.write(self.fd,text.encode())
 def close(self):
  self.stop=True
  self.log.write_bytes(bytes(self.output))
  try:os.kill(self.pid,signal.SIGTERM)
  except ProcessLookupError:pass
  os.waitpid(self.pid,0);os.close(self.fd);self.thread.join(1)
