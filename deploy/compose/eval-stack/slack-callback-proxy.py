#!/usr/bin/env python3
"""Path-filtering forwarder for the Slack approval callback.

Exposing all of strazad through a public tunnel just to receive Slack's
Approve/Deny button POSTs is more surface than the demo needs. This proxy
forwards EXACTLY ONE route, POST /v1/approval/callbacks/slack, to the
loopback-bound strazad and answers 404 to everything else, so a quick
tunnel pointed at it can never reach the admin API.

Usage (the Slack settings are on https://docs.straza.ai/guides/approve/slack/):

    python3 slack-callback-proxy.py            # listens on 127.0.0.1:8899
    cloudflared tunnel --url http://127.0.0.1:8899

Point the Slack app's Interactivity Request URL at
    https://<printed-tunnel-host>/v1/approval/callbacks/slack

Stdlib only; no TLS here (the tunnel terminates HTTPS). The forwarded
request keeps body and headers intact: strazad still verifies the
X-Slack-Signature HMAC and the 5-minute replay window, so this proxy adds
reachability, not trust.
"""

import http.client
import http.server

UPSTREAM = ("127.0.0.1", 8420)
ALLOWED_PATH = "/v1/approval/callbacks/slack"
LISTEN = ("127.0.0.1", 8899)
MAX_BODY = 1 << 20  # mirror strazad's 1 MiB callback cap


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self):
        if self.path != ALLOWED_PATH:
            self.send_error(404)
            return
        length = min(int(self.headers.get("Content-Length", 0)), MAX_BODY)
        body = self.rfile.read(length)
        conn = http.client.HTTPConnection(*UPSTREAM, timeout=15)
        try:
            fwd = {
                k: v
                for k, v in self.headers.items()
                if k.lower()
                in ("content-type", "x-slack-signature", "x-slack-request-timestamp")
            }
            fwd["Content-Length"] = str(len(body))
            conn.request("POST", ALLOWED_PATH, body=body, headers=fwd)
            resp = conn.getresponse()
            data = resp.read()
            self.send_response(resp.status)
            self.send_header("Content-Type", resp.getheader("Content-Type", "application/json"))
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except OSError:
            self.send_error(502)
        finally:
            conn.close()

    # anything that isn't the one allowed POST is not our business
    def do_GET(self):
        self.send_error(404)

    do_PUT = do_DELETE = do_PATCH = do_HEAD = do_GET

    def log_message(self, fmt, *args):  # quiet: one line per forwarded call only
        if self.path == ALLOWED_PATH:
            super().log_message(fmt, *args)


if __name__ == "__main__":
    print(f"forwarding POST {ALLOWED_PATH} -> {UPSTREAM[0]}:{UPSTREAM[1]} "
          f"(listening on {LISTEN[0]}:{LISTEN[1]}, everything else 404)")
    http.server.ThreadingHTTPServer(LISTEN, Handler).serve_forever()
