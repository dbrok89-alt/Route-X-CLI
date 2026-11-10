#!/usr/bin/env python3
"""Fake LSP server for tests: speaks JSON-RPC over stdio with
Content-Length framing. Answers initialize, definition,
references, hover and documentSymbol with fixed data."""
import json, sys

def read_msg():
    length = None
    while True:
        line = sys.stdin.buffer.readline()
        if not line:
            return None
        line = line.decode().strip()
        if line == "":
            break
        if line.lower().startswith("content-length:"):
            length = int(line.split(":", 1)[1].strip())
    if length is None:
        return None
    return json.loads(sys.stdin.buffer.read(length))

def send(obj):
    data = json.dumps(obj).encode()
    sys.stdout.buffer.write(b"Content-Length: %d\r\n\r\n" % len(data) + data)
    sys.stdout.buffer.flush()

while True:
    msg = read_msg()
    if msg is None:
        break
    method = msg.get("method", "")
    mid = msg.get("id")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"capabilities": {}}})
    elif method == "shutdown":
        send({"jsonrpc": "2.0", "id": mid, "result": None})
    elif method == "exit":
        break
    elif method == "textDocument/definition":
        send({"jsonrpc": "2.0", "id": mid, "result": [
            {"uri": "file:///tmp/def.go", "range": {"start": {"line": 9, "character": 5}, "end": {"line": 9, "character": 9}}}]})
    elif method == "textDocument/references":
        send({"jsonrpc": "2.0", "id": mid, "result": [
            {"uri": "file:///tmp/a.go", "range": {"start": {"line": 1, "character": 0}, "end": {"line": 1, "character": 4}}},
            {"uri": "file:///tmp/b.go", "range": {"start": {"line": 3, "character": 2}, "end": {"line": 3, "character": 6}}}]})
    elif method == "textDocument/hover":
        send({"jsonrpc": "2.0", "id": mid, "result": {"contents": {"kind": "markdown", "value": "func Foo() int"}}})
    elif method == "textDocument/documentSymbol":
        send({"jsonrpc": "2.0", "id": mid, "result": [
            {"name": "Foo", "kind": 12, "range": {"start": {"line": 0, "character": 0}, "end": {"line": 5, "character": 1}}, "selectionRange": {"start": {"line": 0, "character": 5}, "end": {"line": 0, "character": 8}}},
            {"name": "Bar", "kind": 12, "range": {"start": {"line": 6, "character": 0}, "end": {"line": 9, "character": 1}}, "selectionRange": {"start": {"line": 6, "character": 5}, "end": {"line": 6, "character": 8}}}]})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "result": None})
