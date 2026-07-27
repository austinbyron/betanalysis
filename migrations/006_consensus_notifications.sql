-- Dedup ledger for Discord consensus notifications: one row per message
-- actually sent (initial pick + optional strong upgrade), inserted only
-- after Discord accepts the webhook POST. Applied idempotently on every
-- deploy by bootstrap-pi.sh.

CREATE TABLE IF NOT EXISTS consensus_notifications (
    id SERIAL PRIMARY KEY,
    game_id VARCHAR(64) NOT NULL REFERENCES games(id),
    selection VARCHAR(64) NOT NULL,
    strong BOOLEAN NOT NULL,
    sent_at TIMESTAMP NOT NULL DEFAULT (timezone('UTC', NOW()))
);

CREATE INDEX IF NOT EXISTS idx_consensus_notifications_game
    ON consensus_notifications(game_id);
