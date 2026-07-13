-- migrations/004_api_quota.sql
-- Last-seen Odds API credit counters (x-requests-remaining/-used response
-- headers), single row updated on every API call so the dashboard can show
-- quota without spending a request.

CREATE TABLE IF NOT EXISTS api_quota (
    id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    requests_remaining DECIMAL(12, 2) NOT NULL,
    requests_used DECIMAL(12, 2) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
