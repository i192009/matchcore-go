# ADR 001: One fenced writer per instrument

Status: accepted.

Matching priority must be a total order; concurrent order-book mutation makes that order difficult to prove. One goroutine owns each instrument and receives a bounded channel of commands. Separate books can plan concurrently because they share no mutable matching state. Financial coordination remains in PostgreSQL because accounts share quote assets across instruments.

A session advisory lock prevents ordinary duplicate owners. A durable epoch checked under the instrument row lock fences a prior owner after connection loss and replacement. In-memory reads use immutable committed views. Uncertain outcomes stop the writer; restart reconstructs state before readiness.

This favors inspectable correctness over active-active claims. The current executable owns all configured instruments, and losing one makes overall readiness fail. Automatic election, instrument placement and regional failover are future work.
