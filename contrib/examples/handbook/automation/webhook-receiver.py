#!/usr/bin/env python3
# A receiver for halite's webhook returner, in forty lines of the standard
# library, that checks the signature the way any receiver must.
#
#     python3 webhook-receiver.py SECRET_FILE CERT.pem KEY.pem
#
# The returner (or `event_return: webhook` on a hub) POSTs JSON over HTTPS
# with these headers:
#
#     X-Halite-Timestamp   when it was signed
#     X-Halite-Signature   hex HMAC-SHA-256, keyed with the shared secret, of
#                          the timestamp, a ".", and the raw body
#     X-Halite-Nonce       random, for replay detection
#     X-Halite-Node, X-Halite-Kind   which node, and what kind of record
#
# Verify BEFORE parsing the body, with a constant-time comparison, and refuse
# a timestamp that is too old. This receiver does the first two; a real one
# does the third too.
import hashlib
import hmac
import http.server
import json
import ssl
import sys

secret = open(sys.argv[1]).read().strip().encode()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        stamp = self.headers.get("X-Halite-Timestamp", "")
        got = self.headers.get("X-Halite-Signature", "")
        want = hmac.new(secret, stamp.encode() + b"." + body, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(got, want):
            self.send_response(401)
            self.end_headers()
            return
        print(json.dumps({"path": self.path, "node": self.headers.get("X-Halite-Node"),
                          "record": json.loads(body)}), flush=True)
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):
        pass


server = http.server.HTTPServer(("127.0.0.1", 14599), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(sys.argv[2], sys.argv[3])
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
