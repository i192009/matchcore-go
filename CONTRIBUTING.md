# Contributing

Run the isolated test dependencies described in `docs/deployment.md`, then Go unit/race/integration tests, `make lint`, frontend build/tests, `buf lint` and the Compose acceptance scenario. Preserve numeric, conservation, priority, idempotency and replay assertions when changing matching or settlement. Record benchmark commands and actual output when making performance claims.

Keep matching deterministic and independent of I/O. Every external decision that affects matching must enter the recorded command/event data. Add event schema/configuration migrations before changing durable data interpretation. Never retry an uncertain financial operation under a new key. Do not commit `.env`, credentials, binaries, local volumes or profile binaries.

Use the existing domain boundaries. Explain the risk being addressed, resulting behavior and actual validation in pull requests. The current CI workflow runs backend static/race/real-service checks, frontend checks, security scans, Docker startup, acceptance restart and browser checks; its successful local equivalents are recorded in `docs/verification.md`.
