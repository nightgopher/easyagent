#!/usr/bin/env python3
"""Install and exercise a server deb in a disposable systemd VM/CI runner.

All model and Feishu traffic terminates at local fixtures; no real credentials,
messages or model calls are used. Refuses an existing EasyAgent installation.
"""
import argparse
import base64
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import select
import socket
import ssl
import struct
import subprocess
import tempfile
import threading
import time
import urllib.request


def run(*args, check=True):
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=110)
    if result.stdout:
        print(result.stdout, flush=True)
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} failed ({result.returncode})")
    return result


def wait_for(check, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError):
            pass
        time.sleep(0.2)
    raise AssertionError("condition did not become true")


class Fixtures:
    def __init__(self):
        self.started = []
        self.feishu_ready = threading.Event()
        self.model_attempts = 0
        self.results = []
        fixture = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def reply(self, body, status=200):
                body = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                if self.path.startswith("/feishu-ws"):
                    accept = base64.b64encode(hashlib.sha1((self.headers["Sec-WebSocket-Key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
                    self.send_response(101)
                    self.send_header("Upgrade", "websocket")
                    self.send_header("Connection", "Upgrade")
                    self.send_header("Sec-WebSocket-Accept", accept)
                    self.end_headers()
                    fixture.feishu_ready.set()
                    while self.rfile.read(1):
                        pass
                else:
                    self.reply({"data": [{"id": "qa-model", "context_window": 32000}]})

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
                if self.path == "/callback/ws/endpoint":
                    self.reply({"code": 0, "data": {"URL": f"ws://127.0.0.1:{fixture.http.server_port}/feishu-ws?device_id=qa&service_id=1", "ClientConfig": {"PingInterval": 120}}})
                    return
                messages = body["messages"]
                last_user = max(i for i, message in enumerate(messages) if message["role"] == "user")
                prompt = messages[last_user]["content"]
                if isinstance(prompt, list):
                    prompt = " ".join(block.get("text", "") for block in prompt)
                tools = [m for m in messages[last_user + 1:] if m["role"] == "tool"]
                if "qa foreground" in prompt and not tools:
                    fixture.model_attempts += 1
                    if fixture.model_attempts <= 2:
                        self.reply({"error": "temporary QA upstream failure"}, [429, 503][fixture.model_attempts - 1])
                        return
                plan = [("bash", {"command": "printf 'QA_FOREGROUND\\n'; touch /var/lib/easyagent/qa-write"})]
                if "qa background" in prompt:
                    plan = [("bash", {"command": "printf 'QA_BACKGROUND\\n'; sleep 60", "run_in_background": True}), ("bash_output", {"task_id": "bg-1"}), ("bash_kill", {"task_id": "bg-1"})]
                index = len(tools)
                if tools:
                    fixture.results.append(tools[-1]["content"])
                if index < len(plan):
                    name, args = plan[index]
                    delta = {"tool_calls": [{"index": 0, "id": f"qa-{index}", "type": "function", "function": {"name": name, "arguments": json.dumps(args)}}]}
                    reason = "tool_calls"
                else:
                    delta, reason = {"content": "QA_COMPLETE"}, "stop"
                chunks = [{"choices": [{"index": 0, "delta": delta}]}, {"choices": [{"index": 0, "delta": {}, "finish_reason": reason}]}]
                payload = "".join("data: " + json.dumps(chunk) + "\n\n" for chunk in chunks) + "data: [DONE]\n\n"
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(payload.encode())))
                self.end_headers()
                self.wfile.write(payload.encode())

        self.http = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.tls = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)

        class Proxy(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_CONNECT(self):
                if self.path != "open.feishu.cn:443":
                    self.send_error(403)
                    return
                with socket.create_connection(("127.0.0.1", fixture.tls.server_port)) as upstream:
                    self.send_response(200, "Connection established")
                    self.end_headers()
                    while True:
                        ready, _, _ = select.select([upstream, self.connection], [], [], 30)
                        if not ready:
                            return
                        for source in ready:
                            data = source.recv(65536)
                            if not data:
                                return
                            (self.connection if source is upstream else upstream).sendall(data)

        self.proxy = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Proxy)

    def start(self, certificate, key):
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certificate, key)
        self.tls.socket = context.wrap_socket(self.tls.socket, server_side=True)
        for server in (self.http, self.tls, self.proxy):
            threading.Thread(target=server.serve_forever, daemon=True).start()
            self.started.append(server)

    def close(self):
        for server in (self.http, self.tls, self.proxy):
            if server in self.started:
                server.shutdown()
            server.server_close()


def api(path, body=None):
    request = urllib.request.Request("http://127.0.0.1:8080" + path,
        data=None if body is None else json.dumps(body).encode(),
        headers={"Authorization": "Bearer qa-service-token", "Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=25) as response:
        return json.load(response)


def check_terminal(session):
    # Minimal RFC 6455 client keeps this smoke test dependency-free.
    with socket.create_connection(("127.0.0.1", 8080), timeout=10) as connection:
        key = base64.b64encode(os.urandom(16)).decode()
        connection.sendall((f"GET /terminal?session_id={session} HTTP/1.1\r\nHost: 127.0.0.1:8080\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: {key}\r\nAuthorization: Bearer qa-service-token\r\n\r\n").encode())
        stream = connection.makefile("rb")
        assert b"101" in stream.readline()
        while stream.readline() != b"\r\n":
            pass

        def send(value):
            payload = json.dumps(value).encode()
            mask = os.urandom(4)
            length = bytes([0x80 | len(payload)]) if len(payload) < 126 else bytes([0xfe]) + struct.pack("!H", len(payload))
            connection.sendall(bytes([0x81]) + length + mask + bytes(v ^ mask[i % 4] for i, v in enumerate(payload)))

        send({"type": "input", "data": "test -t 0 && printf '%s%s\\n' 'QA_' 'PTY'\r"})
        output = b""
        while b"QA_PTY" not in output:
            first, length = stream.read(2)
            length &= 127
            if length == 126:
                length = struct.unpack("!H", stream.read(2))[0]
            elif length == 127:
                length = struct.unpack("!Q", stream.read(8))[0]
            assert first & 15 != 8, "terminal closed before returning output"
            output += stream.read(length)
            send({"type": "ack"})
        print("PASS: authenticated PTY with PrivateDevices=true", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deb", type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or not Path("/run/systemd/system").exists():
        parser.error("requires root inside a disposable Linux systemd VM")
    config = Path("/etc/easyagent/easyagent.env")
    if config.exists() or Path("/usr/bin/easyagent").exists():
        parser.error("refusing to modify an existing EasyAgent installation")
    fixture = Fixtures()
    baseline = Path("/run/systemd/system/easyagent-qa-baseline.service")
    installed = False
    try:
        run("dpkg", "-i", str(args.deb.resolve()))
        installed = True
        run("systemctl", "stop", "easyagent.service")
        run("systemd-analyze", "verify", "/lib/systemd/system/easyagent.service", "/lib/systemd/system/easyagent-bridge.service")
        with tempfile.TemporaryDirectory(prefix="easyagent-unit-qa-") as temporary:
            key = Path(temporary) / "key.pem"
            certificate = Path("/var/lib/easyagent/qa-ca.pem")
            run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=open.feishu.cn", "-addext", "subjectAltName=DNS:open.feishu.cn", "-keyout", str(key), "-out", str(certificate))
            certificate.chmod(0o644)
            fixture.start(certificate, key)
            config.write_text(f"""EA_PROVIDER=openai
EA_MODEL=qa-model
EA_BASE_URL=http://127.0.0.1:{fixture.http.server_port}
EA_API_KEY=qa-upstream-key
EA_SERVER_API_KEY=qa-service-token
EA_HOME=/var/lib/easyagent
EA_DATA_DIR=/var/lib/easyagent/data
EA_WORKSPACE=/var/lib/easyagent
EA_ENABLE_BASH=true
EA_ENABLE_TERMINAL=1
EA_SKILL_MARKET=0
FEISHU_APP_ID=qa-app
FEISHU_APP_SECRET=qa-secret
SSL_CERT_FILE={certificate}
HTTPS_PROXY=http://127.0.0.1:{fixture.proxy.server_port}
NO_PROXY=localhost,127.0.0.1
""")
            run("systemctl", "start", "easyagent.service")
            wait_for(lambda: api("/health")["status"] == "ok")
            print("PASS: installed deb, hardened service and /health", flush=True)
            for prompt in ("qa foreground", "qa background"):
                result = api("/chat", {"prompt": prompt})
                assert "QA_COMPLETE" in result["text"], result
            assert fixture.model_attempts == 3
            assert any("QA_FOREGROUND" in str(value) for value in fixture.results)
            assert any("QA_BACKGROUND" in str(value) for value in fixture.results)
            assert Path("/var/lib/easyagent/qa-write").exists()
            assert json.loads(Path("/var/lib/easyagent/data/background-tasks.json").read_text()) == []
            print("PASS: 429/503 recovery, foreground and background bash start/output/kill", flush=True)
            check_terminal(result["session_id"])
            run("systemctl", "start", "easyagent-bridge.service")
            assert fixture.feishu_ready.wait(20), "Feishu fixture did not complete the WebSocket handshake"
            time.sleep(0.5)
            run("systemctl", "is-active", "easyagent.service", "easyagent-bridge.service")
            journal = run("journalctl", "-u", "easyagent-bridge.service", "--no-pager", "-n", "30").stdout
            assert "feishu WebSocket connected" in journal
            print("PASS: hardened bridge connects to local TLS/bootstrap + WebSocket fixture", flush=True)
            properties = {"CapabilityBoundingSet", "AmbientCapabilities", "PrivateDevices", "ProtectKernelLogs", "ProtectClock", "ProtectHostname", "LockPersonality", "RestrictRealtime"}
            unit = Path("/lib/systemd/system/easyagent.service").read_text()
            baseline.write_text("\n".join(line for line in unit.splitlines() if line.split("=", 1)[0] not in properties) + "\n")
            run("systemctl", "daemon-reload")
            report = run("systemd-analyze", "security", "--no-pager", "easyagent-qa-baseline.service", "easyagent.service", check=False).stdout
            scores = re.findall(r"Overall exposure level.*?:\s*([\d.]+)", report)
            assert len(scores) == 2 and float(scores[1]) <= float(scores[0]), scores
            print(f"PASS: systemd security exposure {scores[0]} -> {scores[1]}", flush=True)
    finally:
        if installed:
            run("systemctl", "stop", "easyagent-bridge.service", "easyagent.service", check=False)
            run("dpkg", "--purge", "easyagent-server", check=False)
        baseline.unlink(missing_ok=True)
        run("systemctl", "daemon-reload", check=False)
        fixture.close()


if __name__ == "__main__":
    main()
