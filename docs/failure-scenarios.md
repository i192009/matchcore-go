# Failure behavior and evidence

| Failure | Required behavior | Executable evidence |
|---|---|---|
| Invalid order / inadequate funds | Commit rejection, keep book and balances unchanged | Core validation; competing reservations test |
| Cross-instrument USD race | Ordered SQL locks prevent double reservation | `TestCrossInstrumentReservationsAndDuplicates` |
| SQL error before commit | Roll back every financial record; discard plan | `TestRealSQLFailureRollsBackBook` |
| Crash before commit | No committed event or visible mutation | `TestCrashBeforeCommitAndAfterCommit/false` |
| Commit succeeds before apply fails | Readiness false; restart replays exactly once financially | `TestCrashBeforeCommitAndAfterCommit/true` |
| Commit succeeds before publication | Outbox remains pending | `TestOutboxRetryAndRealDependencies` |
| Broker ack before publisher crash | Repeat with stable ID; no settlement consumer | `TestCrashAfterBrokerAckAndDuplicateDelivery` |
| Repeated request | Original response; no new financial effects | Acceptance restart; concurrent duplicate test |
| Second writer / stale epoch | Claim rejected / old writer fenced | `TestFencingAndDatabaseUnavailability` |
| Cancel competes with fill | Sequential outcome, no overfill or stranded reservation | `TestCancellationCompetingWithExecution` |
| Saturated queue / expired queued request | Backpressure / no transaction begins | `TestBoundedQueueAndExpiredRequests` |
| Invalid recovery data | Fail closed | `TestSnapshotAndReplayValidation` |
| Slow subscriber | Cancel connection after bounded queue overflow | Transport tests |
| Concurrent subscriber removal | No blocked writer or data race | `TestConcurrentSubscriberDisconnects` |
| WebSocket duplicate / gap | Ignore duplicate; resnapshot on gap | Frontend stream tests |
| Wrong origin / private account subscription | Deny upgrade / close with policy violation | Transport tests |
| Database connection / commit outcome unknown | Reject commands or stop unsafe writer | SQL failure hooks; epoch test; restart acceptance |

There is no claim that synthetic failure injection exhaustively tests network partitions or hardware failure. Integration tests use real services with controlled SQL and broker failure points. Abrupt operating-system termination and PostgreSQL failover should also be exercised under the intended deployment environment before making stronger availability claims.
