CREATE TABLE IF NOT EXISTS instruments (
 id text PRIMARY KEY, sequence bigint NOT NULL DEFAULT 0, epoch bigint NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS accounts (id text PRIMARY KEY, restricted boolean NOT NULL DEFAULT false);
CREATE TABLE IF NOT EXISTS balances (
 account text REFERENCES accounts(id), asset text NOT NULL,
 available bigint NOT NULL CHECK(available>=0), reserved bigint NOT NULL CHECK(reserved>=0),
 PRIMARY KEY(account,asset)
);
CREATE TABLE IF NOT EXISTS supply(asset text PRIMARY KEY, issued bigint NOT NULL CHECK(issued>=0));
CREATE TABLE IF NOT EXISTS orders (
 id text PRIMARY KEY, account text NOT NULL REFERENCES accounts(id), client_id text NOT NULL,
 instrument text NOT NULL REFERENCES instruments(id), status text NOT NULL, data jsonb NOT NULL,
 UNIQUE(account,client_id)
);
CREATE INDEX IF NOT EXISTS orders_account ON orders(account,instrument);
CREATE TABLE IF NOT EXISTS events (
 id text PRIMARY KEY, instrument text NOT NULL REFERENCES instruments(id), sequence bigint NOT NULL,
 schema_version int NOT NULL CHECK(schema_version=1), correlation_id text NOT NULL,
 timestamp bigint NOT NULL, payload bytea NOT NULL, checksum text NOT NULL,
 UNIQUE(instrument,sequence)
);
CREATE TABLE IF NOT EXISTS commands (
 account text NOT NULL REFERENCES accounts(id), key text NOT NULL, digest text NOT NULL,
 response bytea NOT NULL, PRIMARY KEY(account,key)
);
CREATE TABLE IF NOT EXISTS trades (
 id text PRIMARY KEY, instrument text NOT NULL, sequence bigint NOT NULL,
 buyer text NOT NULL, seller text NOT NULL, data jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS trades_instrument ON trades(instrument,sequence DESC);
CREATE TABLE IF NOT EXISTS ledger (
 id bigserial PRIMARY KEY, event_id text NOT NULL REFERENCES events(id),
 account text NOT NULL, asset text NOT NULL, available_delta bigint NOT NULL,
 reserved_delta bigint NOT NULL, kind text NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_account ON ledger(account,id DESC);
CREATE TABLE IF NOT EXISTS settlements (trade_id text PRIMARY KEY REFERENCES trades(id), event_id text NOT NULL REFERENCES events(id));
CREATE TABLE IF NOT EXISTS outbox (
 event_id text PRIMARY KEY REFERENCES events(id), published_at timestamptz,
 attempts int NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(event_id) WHERE published_at IS NULL;
CREATE TABLE IF NOT EXISTS snapshots (
 instrument text NOT NULL, sequence bigint NOT NULL, schema_version int NOT NULL,
 payload bytea NOT NULL, checksum text NOT NULL, PRIMARY KEY(instrument,sequence)
);
-- Event history is append-only, even for the database role that runs the service.
CREATE OR REPLACE FUNCTION reject_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'authoritative events are append-only'; END $$;
DROP TRIGGER IF EXISTS events_immutable ON events;
CREATE TRIGGER events_immutable BEFORE UPDATE OR DELETE ON events FOR EACH ROW EXECUTE FUNCTION reject_event_mutation();
