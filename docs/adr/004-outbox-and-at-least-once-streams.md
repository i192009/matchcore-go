# ADR 004: Keep NATS outside the authoritative commit

Status: accepted.

An SQL trade commit and an independent NATS publish cannot be atomically coordinated by ordinary application calls. Publishing first risks phantom trades; publishing after commit without a journal risks losing events. A SQL outbox inserted with the authoritative envelope closes the loss window.

The publisher waits for a JetStream durable acknowledgement before marking SQL delivery. A crash between those steps repeats delivery. Stable broker message IDs reduce duplicates within a configured window, while instrument sequence numbers support indefinite consumer deduplication and gap detection. No settlement consumer exists, so duplicate market delivery cannot duplicate money movements.

Redis is an expiring public cache and can be rebuilt or discarded. Gateway instances need distinct durable consumer identities. Stream/database backups and retention policies are operational concerns; no exactly-once external transport claim is made.
