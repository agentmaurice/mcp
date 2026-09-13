"""Deterministic embedding fixture; never a production embedding provider."""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        size = int(self.headers.get("Content-Length", "0"))
        if size > 65536:
            self.send_error(413)
            return
        text = str(json.loads(self.rfile.read(size)).get("input", "")).lower()
        contract = any(word in text for word in ("contrat", "contract", "résil", "supplier", "subscription", "cancel"))
        holiday = any(word in text for word in ("vacation", "leave", "vacances", "congé"))
        vector = [1.0, 0.0, 0.1] if contract else [0.0, 1.0, 0.1] if holiday else [0.0, 0.0, 1.0]
        body = json.dumps({"embedding": vector}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
