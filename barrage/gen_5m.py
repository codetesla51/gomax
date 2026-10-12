#!/usr/bin/env python3
"""Generate barrage/endurance-5m.yaml: 100 VUs, ~5M requests, spread keys."""
ids = [1] + [i * 5000 for i in range(1, 20)]
L = [
    "# Hold test: 100 concurrent users, ~5M requests, 20 hot keys.",
    "# At ~4k/s this is ~21 minutes.",
    "duration: 25m",
    "bucket_width: 1s",
    "ramp: 30s",
    "concurrency: 100",
    "scenario:",
]
for i in ids:
    L += [
        f"  - name: read-{i:05d}",
        "    weight: 4",
        "    steps:",
        "      - method: GET",
        f"        url: http://localhost:8080/v1/items/{i}",
    ]
L += [
    "  - name: health",
    "    weight: 10",
    "    steps:",
    "      - method: GET",
    "        url: http://localhost:8080/healthz",
    "  - name: create-item",
    "    weight: 10",
    "    steps:",
    "      - method: POST",
    "        url: http://localhost:8080/v1/items",
    '        body: \'{"name":"bench","price_cents":99}\'',
    "        headers: {Content-Type: application/json}",
]
open("barrage/endurance-5m.yaml", "w").write("\n".join(L) + "\n")
print("wrote endurance-5m.yaml")
