-- migrations/001_create_tables.sql
-- Create tables for the betting analysis system

-- Games table
CREATE TABLE IF NOT EXISTS games (
    id VARCHAR(64) PRIMARY KEY,
    sport_key VARCHAR(64) NOT NULL,
    commence_time TIMESTAMP NOT NULL,
    home_team VARCHAR(128) NOT NULL,
    away_team VARCHAR(128) NOT NULL,
    status VARCHAR(32) DEFAULT 'scheduled',
    home_score INT,
    away_score INT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_games_sport_status ON games(sport_key, status);
CREATE INDEX IF NOT EXISTS idx_games_commence ON games(commence_time);

-- Game odds table
CREATE TABLE IF NOT EXISTS game_odds (
    id SERIAL PRIMARY KEY,
    game_id VARCHAR(64) NOT NULL REFERENCES games(id),
    bookmaker VARCHAR(64) NOT NULL,
    market_type VARCHAR(32) NOT NULL,
    home_odds DECIMAL(10, 4),
    away_odds DECIMAL(10, 4),
    draw_odds DECIMAL(10, 4),
    home_spread DECIMAL(10, 4),
    away_spread DECIMAL(10, 4),
    over_under DECIMAL(10, 4),
    over_odds DECIMAL(10, 4),
    under_odds DECIMAL(10, 4),
    last_update TIMESTAMP,
    retrieved_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(game_id, bookmaker, market_type)
);

CREATE INDEX IF NOT EXISTS idx_odds_game_id ON game_odds(game_id);
CREATE INDEX IF NOT EXISTS idx_odds_retrieved ON game_odds(retrieved_at DESC);

-- Portfolios table
CREATE TABLE IF NOT EXISTS portfolios (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL DEFAULT 'Default Portfolio',
    balance DECIMAL(12, 2) NOT NULL DEFAULT 0,
    initial_bankroll DECIMAL(12, 2) NOT NULL DEFAULT 0,
    bets_placed INT NOT NULL DEFAULT 0,
    bets_won INT NOT NULL DEFAULT 0,
    bets_lost INT NOT NULL DEFAULT 0,
    total_wagered DECIMAL(14, 2) NOT NULL DEFAULT 0,
    total_profit_loss DECIMAL(14, 2) NOT NULL DEFAULT 0,
    active_bets_count INT NOT NULL DEFAULT 0,
    active_bets_value DECIMAL(14, 2) NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Bets table
CREATE TABLE IF NOT EXISTS bets (
    id VARCHAR(64) PRIMARY KEY,
    portfolio_id VARCHAR(64) NOT NULL REFERENCES portfolios(id),
    game_id VARCHAR(64) NOT NULL REFERENCES games(id),
    selection VARCHAR(16) NOT NULL,
    market_type VARCHAR(32) NOT NULL,
    odds DECIMAL(10, 4) NOT NULL,
    stake DECIMAL(12, 2) NOT NULL,
    potential_win DECIMAL(12, 2) NOT NULL,
    actual_win DECIMAL(12, 2),
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    expected_value DECIMAL(10, 6),
    model_id VARCHAR(64),
    bookmaker VARCHAR(64),
    notes TEXT,
    settled_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bets_portfolio ON bets(portfolio_id);
CREATE INDEX IF NOT EXISTS idx_bets_game ON bets(game_id);
CREATE INDEX IF NOT EXISTS idx_bets_status ON bets(status);

-- Team statistics table
CREATE TABLE IF NOT EXISTS team_stats (
    id VARCHAR(64) PRIMARY KEY,
    team_name VARCHAR(128) NOT NULL,
    sport_key VARCHAR(64) NOT NULL,
    games_played INT NOT NULL DEFAULT 0,
    wins INT NOT NULL DEFAULT 0,
    losses INT NOT NULL DEFAULT 0,
    draws INT NOT NULL DEFAULT 0,
    points_scored DECIMAL(10, 2) NOT NULL DEFAULT 0,
    points_allowed DECIMAL(10, 2) NOT NULL DEFAULT 0,
    win_rate DECIMAL(5, 4) NOT NULL DEFAULT 0,
    last_updated TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(team_name, sport_key)
);

CREATE INDEX IF NOT EXISTS idx_team_stats_sport ON team_stats(sport_key);

-- Models table
CREATE TABLE IF NOT EXISTS models (
    id VARCHAR(64) PRIMARY KEY,
    type VARCHAR(32) NOT NULL,
    name VARCHAR(128) NOT NULL,
    config TEXT,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Job history table for tracking scheduled jobs
CREATE TABLE IF NOT EXISTS job_history (
    id SERIAL PRIMARY KEY,
    job_name VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL,
    started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    error_message TEXT,
    records_processed INT DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_job_history_time ON job_history(started_at DESC);
