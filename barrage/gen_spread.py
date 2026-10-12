#!/usr/bin/env python3
"""Generate barrage/capacity-spread.yaml: 20 hot keys, not one."""
ids = [1] + [i * 5000 for i in range(1, 20)]
L = [
    "# Spread-key sweep: 20 hot keys across the table, not one.",
    "# IDs 1, 5000..95000 (all exist; rows are never deleted).",
    "duration: 30s",
    "bucket_width: 1s",
    "concurrency: 5",
    "capacity:",
    "  max_concurrency: 200",
    "  step_duration: 20s",
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
open("barrage/capacity-spread.yaml", "w").write("\n".join(L) + "\n")
print("wrote", len(ids), "read scenarios")
