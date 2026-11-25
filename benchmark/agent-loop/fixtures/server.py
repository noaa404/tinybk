import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


class FixtureServer:
    def __init__(self, port, pages_dir):
        self.port = port
        self.pages_dir = Path(pages_dir).resolve()
        self.events = []
        self.lock = threading.Lock()
        self.httpd = None
        self.thread = None

    def start(self):
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def send_body(self, code, body, content_type='text/html; charset=utf-8'):
                data = body.encode()
                self.send_response(code)
                self.send_header('Content-Type', content_type)
                self.send_header('Cache-Control', 'no-store')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                u = urlparse(self.path)
                q = parse_qs(u.query)
                if u.path == '/event':
                    event = {k: q.get(k, [''])[0] for k in q}
                    with fixture.lock:
                        fixture.events.append(event)
                    self.send_body(204, '')
                    return

                rel = u.path.lstrip('/') or 'index.html'
                candidate = fixture.pages_dir / rel
                if not candidate.suffix:
                    candidate = candidate.with_suffix('.html')
                try:
                    candidate = candidate.resolve()
                    candidate.relative_to(fixture.pages_dir)
                except (ValueError, OSError):
                    self.send_body(404, 'not found', 'text/plain')
                    return
                if not candidate.is_file():
                    self.send_body(404, 'not found', 'text/plain')
                    return
                ctype = 'text/html; charset=utf-8' if candidate.suffix == '.html' else 'text/plain'
                self.send_body(200, candidate.read_text(), ctype)

        self.httpd = ThreadingHTTPServer(('127.0.0.1', self.port), Handler)
        self.httpd.fixture = self
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def stop(self):
        if self.httpd:
            self.httpd.shutdown()
            self.httpd.server_close()

    def clear_events(self):
        with self.lock:
            self.events.clear()

    def snapshot_events(self):
        with self.lock:
            return list(self.events)
