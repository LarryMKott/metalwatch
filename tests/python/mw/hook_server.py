# -*- coding: utf-8 -*-
"""Webhook 捕获服务（由 hook.WebhookReceiver 以子进程方式拉起）。

记录每次 POST 的关键头与请求体，回 204；GET /__ping 用于就绪探测。
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 18101
CAPTURE = sys.argv[2] if len(sys.argv) > 2 else "hook_capture.jsonl"


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        rec = {
            "path": self.path,
            "event": self.headers.get("X-MetalWatch-Event", ""),
            "sig": self.headers.get("X-MetalWatch-Signature", ""),
            "body": body.decode("utf-8", "replace"),
        }
        with open(CAPTURE, "a", encoding="utf-8") as f:
            f.write(json.dumps(rec) + "\n")
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        self.send_response(200)
        self.end_headers()

    def log_message(self, *args):
        pass


HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
