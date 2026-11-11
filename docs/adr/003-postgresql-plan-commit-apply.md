# ADR 003: PostgreSQL financial commit before book application

Status: accepted.

A fast in-memory mutation followed by a failed SQL write could expose executions without corresponding balances. MatchCore instead plans without mutation, locks instrument and financial rows, validates risk, persists all effects and authoritative events in one transaction, then applies that exact committed plan to memory.

Sorted account/balance locks prevent cross-instrument double spending and reduce deadlock risk. Uniqueness and durable command results make retries safe. Events capture matching decisions; recovery never repeats financial settlement. A failed or ambiguous post-commit apply stops readiness until replay.

Durable matching necessarily includes database round trips, locks, WAL flush, serialization, projections and snapshots. Its latency is different from pure algorithm latency. Both are measured separately, including the contention cost of one shared account.
