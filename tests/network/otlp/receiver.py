import base64
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self) -> None:
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        print(f'OTLP {self.path} {base64.b64encode(body).decode()}', flush=True)
        self.send_response(200)
        self.send_header('Content-Type', 'application/x-protobuf')
        self.send_header('Content-Length', '0')
        self.end_headers()

    def log_message(self, *args: object) -> None:
        pass


server = ThreadingHTTPServer(('0.0.0.0', 4318), Handler)
print('LISTENING', flush=True)
sys.stdout.flush()
server.serve_forever()
