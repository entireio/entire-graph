"""Independent P2 feasibility fixture; run as an unprivileged Linux user.
Origin: authored from the plan and LSP 3.17, no external implementation.
"""
import json, os, pathlib, select, subprocess, tempfile, time

with tempfile.TemporaryDirectory(prefix="graph-probe-") as directory:
    root = pathlib.Path(directory)
    source = root / "source"
    source.mkdir()
    (source / "go.mod").write_text("module fixture.local/probe\n\ngo 1.24\n")
    body = "package probe\nfunc Target() {}\nfunc Caller() { Target() }\n"
    (source / "a.go").write_text(body)
    sandbox = ["bwrap", "--unshare-all", "--die-with-parent", "--new-session",
               "--clearenv", "--ro-bind", "/usr", "/usr", "--ro-bind", "/lib", "/lib",
               "--ro-bind", "/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev",
               "--tmpfs", "/tmp", "--ro-bind", str(source), "/workspace",
               "--ro-bind", "/opt/graph-tools", "/tools", "--chdir", "/workspace",
               "--setenv", "PATH", "/usr/local/go/bin:/usr/bin",
               "--setenv", "GOPATH", "/tmp/gopath", "--setenv", "GOCACHE", "/tmp/gocache",
               "--setenv", "XDG_CACHE_HOME", "/tmp/cache", "--setenv", "XDG_CONFIG_HOME", "/tmp/config",
               "--setenv", "GOPROXY", "off", "--setenv", "GOSUMDB", "off",
               "--setenv", "GOTOOLCHAIN", "local", "--setenv", "GOTELEMETRY", "off"]
    # A grandchild attempts egress as well as a write into captured source.
    negative = subprocess.run(sandbox + ["--", "/usr/bin/python3", "-c",
        "import socket,subprocess; "
        "r=subprocess.run(['/usr/bin/python3','-c',\"import socket; socket.create_connection(('1.1.1.1',443),1)\"],capture_output=True); "
        "assert r.returncode!=0; "
        "print(r.stderr.decode().splitlines()[-1]); "
        "exec(\"try:\\n open('/workspace/a.go','w')\\n raise AssertionError('write allowed')\\nexcept OSError as e:\\n print(type(e).__name__, e.errno)\")"],
        capture_output=True, text=True, timeout=10)
    assert negative.returncode == 0, negative.stderr
    print("isolation:", negative.stdout.strip())
    server = subprocess.Popen(sandbox + ["--", "/tools/gopls", "serve"],
                              stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    deadline = time.monotonic() + 30
    pending = bytearray()
    def send(message):
        payload = json.dumps(message).encode()
        server.stdin.write(f"Content-Length: {len(payload)}\r\n\r\n".encode() + payload)
        server.stdin.flush()
    def receive():
        while True:
            if b"\r\n\r\n" in pending:
                header, rest = pending.split(b"\r\n\r\n", 1)
                size = int(header.split(b":",1)[1].strip())
                assert size <= 8*1024*1024
                if len(rest) >= size:
                    message = json.loads(rest[:size]); pending[:] = rest[size:]; return message
            remaining = deadline - time.monotonic()
            assert remaining > 0, "protocol deadline"
            assert select.select([server.stdout], [], [], remaining)[0], "protocol timeout"
            chunk = os.read(server.stdout.fileno(), 65536)
            assert chunk, "server closed"
            pending.extend(chunk)
    def request(identifier, method, params):
        send(dict(jsonrpc="2.0", id=identifier, method=method, params=params))
        while True:
            message = receive()
            if "method" in message and "id" in message:
                send(dict(jsonrpc="2.0", id=message["id"], error=dict(code=-32601,message="not supported")))
            elif message.get("id") == identifier:
                assert "error" not in message, message
                return message.get("result")
    try:
        result = request(1,"initialize",dict(processId=None,rootUri="file:///workspace",
            capabilities={"general":{"positionEncodings":["utf-16"]}},
            initializationOptions={"telemetryPrompt":False,"checkUpdates":"off","analyses":{},"staticcheck":False}))
        print("server:",json.dumps(result.get("serverInfo")))
        print("positionEncoding:",result["capabilities"].get("positionEncoding","utf-16"))
        send(dict(jsonrpc="2.0",method="initialized",params={}))
        send(dict(jsonrpc="2.0",method="textDocument/didOpen",params={"textDocument":{
            "uri":"file:///workspace/a.go","languageId":"go","version":1,"text":body}}))
        result=request(2,"textDocument/definition",{"textDocument":{"uri":"file:///workspace/a.go"},"position":{"line":2,"character":17}})
        print("definition:",json.dumps(result,sort_keys=True))
        assert result == [{"uri":"file:///workspace/a.go","range":{"start":{"line":1,"character":5},"end":{"line":1,"character":11}}}], result
        request(3,"shutdown",None)
        send(dict(jsonrpc="2.0",method="exit"))
        assert server.wait(timeout=5)==0
        assert (source/"a.go").read_text()==body
        print("P2_FEASIBILITY_OK")
    finally:
        if server.poll() is None:
            server.kill(); server.wait()
