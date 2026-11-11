# Recovery and operator procedure

Readiness starts false. The matching process claims instrument locks, increments epochs, restores the latest snapshots, verifies subsequent event checksums and sequences, applies plans, compares the resulting open orders with the SQL projection, and only then admits commands. One failed instrument prevents overall readiness. State is rebuilt independently per instrument; balances remain authoritative SQL data and are never settled again by replay.

For a clean restart:

```sh
docker compose restart matchcore
docker compose ps
curl -fsS http://localhost:8080/readyz
```

For an actual abrupt process restart in the **local simulation**:

```sh
docker compose kill -s SIGKILL matchcore
docker compose up -d matchcore
```

Do not retry a timed-out financial operation with a new key. Wait for readiness and retry the exact original command. Durable accepted and rejected responses are returned without advancing a sequence, creating another order, moving balances or generating another trade.

The matcher intentionally stops on an uncertain commit or failed state application. Restart it to recover from SQL. The publisher retries failed batches automatically, and gateway clients reconnect and resubscribe for a fresh snapshot. A slow client or sequence gap is a client recovery event, not an engine failure. Redis can be restarted or cleared independently.

If recovery reports corruption, preserve the database and logs. Investigate against backups and event checksums; this repository does not contain an automatic corrupt-snapshot deletion or event-repair command. Do not disable checks to obtain readiness. Restore an operator-verified database backup in a separate environment before resuming. PostgreSQL backup/PITR and replicated broker storage are deployment responsibilities, not implemented HA claims.

`tests/integration` injects rollback before commit, an acknowledged SQL commit before in-memory apply, SQL trigger failures, lost ownership epochs, corrupt snapshots, corrupt events, and broker acknowledgement followed by publisher failure. Valid snapshot, no-snapshot, worker restart, exact retry, repeated delivery and concurrent reservations are executable checks. `scripts/acceptance.ps1` and `.sh` additionally prove a trade, fee transfer, stream delivery, cancellation and a real matching-service restart against Compose.
