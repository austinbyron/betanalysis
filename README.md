# betanalysis

Sports betting odds analysis and paper trading. Collects odds from
[The Odds API](https://the-odds-api.com/), stores line history in PostgreSQL,
recommends positive-EV moneyline bets, sizes stakes with fractional Kelly, and
tracks results in a paper portfolio.

**Paper trading only.** No real money is ever placed.

## How it works

- **Collection** stores games and append-only odds snapshots (line history is
  preserved for backtesting).
- **Team records** are built from final scores in the `team_stats` table, so
  everything the strategies learn survives restarts.
- **Estimators** (`thompson`, `epsilon_greedy`, `historical`) turn team
  records into win probabilities. The selector blends them with the de-vigged
  market probability (`analysis.market_weight`, default 0.7) before computing
  EV — raw win rates alone will hallucinate edges against sharp lines.
- **MLB pitcher adjustment**: when `baseball_mlb` is configured, probable
  starters from MLB's free StatsAPI adjust the model probability before the
  market blend — regressed FIP vs league average, ~0.07 win probability per
  run/9 of starter quality. Lookup failures pass through unadjusted, and
  backtests stay records-only (no historical pitcher store).
- **Staking** is fractional Kelly (default half-Kelly), capped at 5% of
  bankroll. If Kelly says the edge is too small for the minimum stake, the bet
  is skipped.
- **Backtesting** replays stored finished games chronologically using only
  pre-kickoff odds snapshots and the records known at the time — no lookahead.
- **Dashboard** (`server.port`, default 8090): portfolio tiles, live model
  recommendations with suggested Kelly stakes, open positions, results, and a
  bankroll curve. Served by the daemon; on the homelab it's
  `http://betanalysis.homelab`.

## Quickstart

```bash
cp .env.example .env      # add your ODDS_API_KEY
docker compose up -d      # postgres (migrations auto-applied) + daemon
```

Or run pieces by hand against a local Postgres:

```bash
go build -o bin/betanalysis ./cmd/cli

bin/betanalysis collect --all      # fetch games + odds
bin/betanalysis scores             # fetch finals, update team records
bin/betanalysis sim                # one paper trading cycle
bin/betanalysis settle             # settle finished bets
bin/betanalysis report             # portfolio status
bin/betanalysis backtest --model historical --days 90
```

The daemon (`./cmd/daemon`) runs everything on schedules: odds collection
during game windows, hourly scores/team-stats updates, 30-minute trading
cycles, hourly settlement.

## Configuration

Defaults live in `internal/config/config.go`. Override via `config.yaml`
(searched in `.`, `~/.betanalysis`, `/etc/betanalysis`) or `BETANALYSIS_*`
environment variables — see `.env.example`. Only `ODDS_API_KEY` is required.

## Pi deployment

`./deploy.sh` cross-compiles arm64 binaries and ships them (plus migrations,
config, and `.env`) to the Pi, then restarts `betanalysis.service`. First-time
setup is `sudo bash ~/betanalysis/bootstrap-pi.sh` on the Pi — installs
PostgreSQL with its data directory on the SSD, creates the database, applies
migrations, installs the systemd unit, adds the Caddy route for
`betanalysis.homelab`, and scopes passwordless sudo for future deploys.
`deploy/config.pi.yaml` is the free-tier trial config: MLB only,
~11 API credits/day via `scheduler.collect_crons`.

## Season priors

New seasons start every team at 0-0, which the warmup gate answers with
silence. Priors fix that, and they seed themselves: on startup the daemon
finds any configured sport with no recorded games and no priors, pulls last
season's final standings from ESPN's free API, regresses each record 1/3
toward .500, and stores it as ~16 pseudo-games per team. Adding
`americanfootball_nfl` to `sports:` in the fall is the whole job.

The same seeding is available by hand (e.g. to reseed or pick a season):

```bash
bin/betanalysis seed-priors --sport americanfootball_nfl            # ESPN standings
bin/betanalysis seed-priors --sport americanfootball_nfl \
    --file win-totals.json --season-games 17                        # market override
```

The optional `--file` override maps team names (exactly as The Odds API
spells them) to projected wins — see `docs/priors-example.json` — for anyone
who wants sharper Vegas win-total priors; The Odds API itself has no
win-total futures market (checked 2026-07: championship winners only).
Reseeding upserts; the Elo model reads game scores and ignores priors.

## Notes

- A meaningful backtest needs accumulated data: run collection for a few
  weeks first. Odds snapshots only exist from the moment you start collecting.
- The Odds API free tier is 500 requests/month — the default schedules are
  sized for a paid tier; trim `sports` or the cron windows in
  `internal/scheduler/scheduler.go` if you're on the free tier. The dashboard
  footer shows credits remaining as of the last API call.
- While the warmup gate holds a model quiet, its would-be picks are recorded
  as preview bets and settled for score — the dashboard's "Warmup preview"
  shadow record answers whether the gate helped or hurt.

## Development

```bash
go test ./...
go vet ./...
```

Tests cover the betting math (Kelly, EV, devig), the estimators, the selector,
the API client against v4 fixture payloads, the trading engine and settlement
against a fake store, and the backtest replay.
