-- migrations/002_append_only_odds.sql
-- Odds become append-only so line-movement history is preserved for
-- backtesting and closing-line-value analysis. The latest line per
-- (game, bookmaker, market) is resolved at query time.

ALTER TABLE game_odds DROP CONSTRAINT IF EXISTS game_odds_game_id_bookmaker_market_type_key;

CREATE INDEX IF NOT EXISTS idx_odds_latest
    ON game_odds(game_id, bookmaker, market_type, retrieved_at DESC);

-- The probability the strategy assigned when recommending the bet, so Kelly
-- sizing and later evaluation use the same number the EV was computed from.
ALTER TABLE bets ADD COLUMN IF NOT EXISTS model_probability DECIMAL(10, 6);
