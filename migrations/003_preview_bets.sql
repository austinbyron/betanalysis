-- migrations/003_preview_bets.sql
-- Shadow bets the warmup confidence gate suppressed. One row per
-- (model, game), written by the trading cycle at the moment it declined
-- to bet, settled like real bets but never touching a portfolio — so the
-- warmup weeks still produce a scoreable track record.

CREATE TABLE IF NOT EXISTS preview_bets (
    id UUID PRIMARY KEY,
    game_id VARCHAR(255) NOT NULL REFERENCES games(id),
    model_id VARCHAR(100) NOT NULL,
    selection VARCHAR(20) NOT NULL,
    market_type VARCHAR(20) NOT NULL,
    bookmaker VARCHAR(100) NOT NULL,
    odds DECIMAL(10, 4) NOT NULL,
    stake DECIMAL(12, 2) NOT NULL,
    model_probability DECIMAL(10, 6) NOT NULL,
    expected_value DECIMAL(10, 6) NOT NULL,
    confidence DECIMAL(10, 6) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    payout DECIMAL(12, 2),
    settled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (model_id, game_id)
);

CREATE INDEX IF NOT EXISTS idx_preview_bets_status ON preview_bets(status);
