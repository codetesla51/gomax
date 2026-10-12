#!/usr/bin/env python3
"""Seed 20 spread rows on LIVE via the API, then write capacity-live.yaml
with their real IDs (live Postgres is only reachable from Render)."""
import json
import urllib.request

BASE = "https://gomax-ndql.onrender.com"
ids = []
for i in range(20):
    req = urllib.request.Request(
        BASE + "/v1/items",
        data=json.dumps({"name": f"seed{i}", "price_cents": 100}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as r:
        ids.append(json.load(r)["id"])
print("seeded ids:", ids)

L = [
    "# LIVE sweep: 20 real keys on the live DB, spread by creation.",
    "duration: 30s",
    "bucket_width: 1s",
    "ramp: 30s",
    "concurrency: 2",
    "capacity:",
    "  max_concurrency: 100",
    "  step_duration: 15s",
    "scenario:",
]
for i in ids:
    L += [
        f"  - name: read-{i}",
        "    weight: 4",
        "    steps:",
        "      - method: GET",
        f"        url: {BASE}/v1/items/{i}",
    ]
L += [
    "  - name: health",
    "    weight: 10",
    "    steps:",
    "      - method: GET",
    f"        url: {BASE}/healthz",
    "  - name: create-item",
    "    weight: 10",
    "    steps:",
    "      - method: POST",
    f"        url: {BASE}/v1/items",
    '        body: \'{"name":"bench","price_cents":99}\'',
    "        headers: {Content-Type: application/json}",
]
open("barrage/capacity-live.yaml", "w").write("\n".join(L) + "\n")
print("wrote barrage/capacity-live.yaml")
