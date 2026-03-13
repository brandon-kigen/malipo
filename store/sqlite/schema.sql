-- Enable WAL mode for concurrent read/write access.
-- Readers do not block writers and writers do not block readers.
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS sessions (
    -- Primary key — ULID, lexicographic and time-ordered
    id                  TEXT PRIMARY KEY NOT NULL,

    -- State machine
    state               TEXT NOT NULL,

    -- Payment details
    phone               TEXT NOT NULL,
    amount              INTEGER NOT NULL,
    currency            TEXT NOT NULL,
    shortcode           TEXT NOT NULL,

    -- Populated after Daraja accepts the STK Push
    checkout_request_id TEXT,
    merchant_request_id TEXT,

    -- Populated after Safaricom confirms payment via callback
    mpesa_receipt_number TEXT,
    confirmed_amount     INTEGER,
    confirmed_phone      TEXT,

    -- Timestamps
    created_at          TEXT NOT NULL,
    expires_at          TEXT NOT NULL,
    consumed_at         TEXT
);

-- Secondary index for callback correlation.
-- GetByCheckoutID is called on every incoming Safaricom callback —
-- without this index it would be a full table scan.
CREATE INDEX IF NOT EXISTS idx_sessions_checkout
ON sessions (checkout_request_id)
WHERE checkout_request_id IS NOT NULL;

-- Partial index for stale session cleanup.
-- ExpireStale filters by expires_at on every cleanup sweep.
-- Partial index excludes terminal sessions — they are never eligible
-- for expiry and would only add noise to the index.
CREATE INDEX IF NOT EXISTS idx_sessions_expiry
ON sessions (expires_at)
WHERE state NOT IN ('CONSUMED', 'TIMEOUT', 'CANCELLED', 'FAILED');