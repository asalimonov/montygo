import base64
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

received = []
lock = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        encoded = base64.b64encode(body).decode()
        with lock:
            received.append({'path': self.path, 'bytes': len(body), 'body': encoded})
        print(f'OTLP {self.path} {encoded}', flush=True)
        self.send_response(200)
        self.send_header('Content-Type', 'application/x-protobuf')
        self.send_header('Content-Length', '0')
        self.end_headers()

    # GET /requests returns what arrived; the container log is only a debugging aid,
    # because docker can lose stdout lines on some runners.
    def do_GET(self) -> None:
        with lock:
            body = json.dumps(received).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args: object) -> None:
        pass


server = ThreadingHTTPServer(('0.0.0.0', 4318), Handler)
print('LISTENING', flush=True)
sys.stdout.flush()
server.serve_forever()
