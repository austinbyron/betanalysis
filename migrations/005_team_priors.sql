-- migrations/005_team_priors.sql
-- Per-team Beta priors seeded from market expectations (preseason win
-- totals) so a new season doesn't start every team at 50/50. Estimators
-- see these as pseudo-games on top of the real record; reseeding upserts.

CREATE TABLE IF NOT EXISTS team_priors (
    team_name VARCHAR(255) NOT NULL,
    sport_key VARCHAR(100) NOT NULL,
    prior_wins DECIMAL(8, 3) NOT NULL,
    prior_losses DECIMAL(8, 3) NOT NULL,
    source VARCHAR(255) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_name, sport_key)
);
