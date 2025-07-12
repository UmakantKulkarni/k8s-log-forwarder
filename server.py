#!/usr/bin/env python3

import argparse
import http.server
import socketserver

LOG_FILE = "received_logs.txt"

def make_handler(endpoint):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, format, *args):
            # suppress default logging to keep output minimal
            pass

        def do_POST(self):
            if self.path != endpoint:
                self.send_response(404)
                self.end_headers()
                return
            length = int(self.headers.get('Content-Length', 0))
            data = self.rfile.read(length)
            with open(LOG_FILE, 'ab') as f:
                f.write(data)
                if not data.endswith(b"\n"):
                    f.write(b"\n")
            self.send_response(204)
            self.end_headers()
    return Handler

if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--host', default='0.0.0.0')
    parser.add_argument('--port', type=int, default=9000)
    parser.add_argument('--endpoint', default='/logs')
    args = parser.parse_args()
    handler = make_handler(args.endpoint)
    with socketserver.TCPServer((args.host, args.port), handler) as httpd:
        print(f"Listening on {args.host}:{args.port}{args.endpoint}")
        httpd.serve_forever()
