-- Consensus record: one row per game where >=3 contenders backed the same
-- side, captured at first appearance (the moment the Discord ping fires)
-- and graded at a flat notional stake. First write wins — the trading
-- cycle inserts with ON CONFLICT DO NOTHING, so vote wobble and side
-- flips never rewrite a pick. Applied idempotently by bootstrap-pi.sh on
-- fresh installs; apply manually via psql on the live Pi (deploy.sh does
-- not run migrations).

CREATE TABLE IF NOT EXISTS consensus_picks (
    id SERIAL PRIMARY KEY,
    game_id VARCHAR(64) NOT NULL UNIQUE REFERENCES games(id),
    selection VARCHAR(64) NOT NULL,
    votes INT,
    total INT,
    strong BOOLEAN NOT NULL DEFAULT FALSE,
    upgraded_to_strong_at TIMESTAMP,
    avg_prob DOUBLE PRECISION,
    min_ev DOUBLE PRECISION,
    best_odds DOUBLE PRECISION NOT NULL,
    best_book VARCHAR(64) NOT NULL,
    stake NUMERIC(10,2) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    pnl NUMERIC(10,2),
    backfilled BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT (timezone('UTC', NOW())),
    settled_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_consensus_picks_status
    ON consensus_picks(status);
