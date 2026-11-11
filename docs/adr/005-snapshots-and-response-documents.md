# ADR 005: Canonical snapshots and one JSON domain representation

Status: accepted.

Snapshots contain open-order state and exact configuration, with schema, sequence and checksum. Canonical price-level/FIFO traversal avoids an expensive global sort and preserves replay order. Snapshot corruption fails closed; operators must verify recovery sources rather than allowing a silent fallback. Event history remains complete.

Public gRPC inputs use generated typed Protobuf messages. Responses carry a JSON domain document inside `Result.json`, sharing the representation used by HTTP, event plans and the TypeScript frontend. This avoids duplicating all domain structures in transport adapters but reduces protobuf response discoverability and requires byte decoding for grpcurl. The OpenAPI document and API guide specify the JSON representation. A fully typed response API is a documented future improvement.
