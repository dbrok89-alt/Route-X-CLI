#!/usr/bin/env python3
"""Minimal fake MCP server for tests: initialize, tools/list, tools/call."""
import json
import sys

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    msg = json.loads(line)
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        result = {"protocolVersion": "2024-11-05", "capabilities": {}, "serverInfo": {"name": "echo", "version": "0.1"}}
    elif method == "tools/list":
        result = {"tools": [{"name": "echo", "description": "Echoes its input"}]}
    elif method == "tools/call":
        if msg.get("params", {}).get("name") == "boom":
            print(json.dumps({"jsonrpc": "2.0", "id": mid, "error": {"code": -32602, "message": "unknown tool boom"}}), flush=True)
            continue
        args = msg.get("params", {}).get("arguments", {})
        result = {"content": [{"type": "text", "text": "echo:" + json.dumps(args)}]}
    elif method == "notifications/initialized":
        continue
    else:
        result = {}
    if mid is not None:
        print(json.dumps({"jsonrpc": "2.0", "id": mid, "result": result}), flush=True)
