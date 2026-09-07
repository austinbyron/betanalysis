-- Provenance for odds snapshots: The Odds API rows stay 'oddsapi'; direct
-- book polls (internal/books) write their source name. Same bookmaker key
-- either way — the engine's latest-per-book query is unchanged.
ALTER TABLE game_odds ADD COLUMN IF NOT EXISTS source VARCHAR(32) NOT NULL DEFAULT 'oddsapi';
