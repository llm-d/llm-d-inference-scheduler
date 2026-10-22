# Copyright 2026 The llm-d Authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Native SGLang HTTP fixture whose prefill body requires a matching decode."""

import http.client
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

pending = {}
lock = threading.Lock()


class Worker(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200 if self.path == "/health" else 404)
        self.end_headers()

    def do_POST(self):
        if self.headers.get("Transfer-Encoding") == "chunked":
            chunks = []
            while True:
                size = int(self.rfile.readline().split(b";", 1)[0], 16)
                if size == 0:
                    while self.rfile.readline() != b"\r\n":
                        pass
                    break
                chunks.append(self.rfile.read(size))
                self.rfile.read(2)
            raw = b"".join(chunks)
        else:
            raw = self.rfile.read(int(self.headers["Content-Length"]))
        body = json.loads(raw)
        room = body.get("bootstrap_room")
        if self.path == "/release":
            with lock:
                prefill, ready = pending.get(room, (None, None))
            if prefill != body:
                self.send_error(400, "decode must preserve the prefill bootstrap fields")
                return
            ready.set()
            self.send_response(200)
            self.end_headers()
            return
        if self.path != "/generate" or not isinstance(room, int):
            self.send_error(400, "expected /generate with a numeric bootstrap room")
            return
        if os.environ["ROLE"] == "prefill":
            if body.get("bootstrap_host") != os.environ["POD_IP"]:
                self.send_error(400, "bootstrap host must be the selected prefill pod")
                return
            ready = threading.Event()
            with lock:
                pending[room] = body, ready
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("x-llm-d-prefiller-host-port", "forged:1")
            self.end_headers()
            self.wfile.flush()
            # Sending no body until decode arrives detects gateway buffering.
            arrived = ready.wait(20)
            with lock:
                del pending[room]
            data = b"data: [DONE]\n\n" if arrived else b'data: {"error":"decode did not arrive"}\n\n'
            self.wfile.write(data)
            return
        conn = http.client.HTTPConnection(body["bootstrap_host"], 8000, timeout=10)
        try:
            conn.request("POST", "/release", json.dumps(body), {"Content-Type": "application/json"})
            response = conn.getresponse()
            if response.status != 200:
                self.send_error(400, response.read().decode())
                return
        finally:
            conn.close()
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        self.wfile.write(b'data: {"text":"decode"}\n\ndata: [DONE]\n\n')


ThreadingHTTPServer(("0.0.0.0", 8000), Worker).serve_forever()
