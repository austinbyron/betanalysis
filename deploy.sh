#!/usr/bin/env bash
# Deploy betanalysis to the Pi. Cross-compiles arm64 binaries, ships them
# with migrations + deploy assets, and restarts the service (passwordless
# via /etc/sudoers.d/betanalysis-deploy after the one-time bootstrap).
#
#   ./deploy.sh            build + ship + restart
#   ./deploy.sh --no-restart   ship only (before first bootstrap)
set -euo pipefail

PI="${BETANALYSIS_PI:?set BETANALYSIS_PI to user@host of your Pi}"
APP_DIR=/home/homelab/betanalysis

echo "== Building linux/arm64 binaries =="
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/daemon ./cmd/daemon
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o build/betanalysis ./cmd/cli

echo "== Shipping to $PI =="
ssh "$PI" "mkdir -p $APP_DIR/migrations"
# Binaries go up under temp names then rename — a running binary can't be
# overwritten in place (ETXTBSY), but rename over it is fine
scp -q build/daemon "$PI:$APP_DIR/daemon.new"
scp -q build/betanalysis "$PI:$APP_DIR/betanalysis.new"
scp -q deploy/betanalysis.service deploy/bootstrap-pi.sh "$PI:$APP_DIR/"
scp -q migrations/*.sql "$PI:$APP_DIR/migrations/"
# Ship the API key if present locally (gitignored on both ends)
[ -f .env ] && scp -q .env "$PI:$APP_DIR/.env"
ssh "$PI" "chmod +x $APP_DIR/daemon.new $APP_DIR/betanalysis.new $APP_DIR/bootstrap-pi.sh && mv $APP_DIR/daemon.new $APP_DIR/daemon && mv $APP_DIR/betanalysis.new $APP_DIR/betanalysis"
# Config: only place the default if none exists — never clobber live config
scp -q deploy/config.pi.yaml "$PI:$APP_DIR/config.pi.yaml"
ssh "$PI" "[ -f $APP_DIR/config.yaml ] || cp $APP_DIR/config.pi.yaml $APP_DIR/config.yaml"

if [ "${1:-}" != "--no-restart" ]; then
    echo "== Restarting service =="
    ssh "$PI" "sudo systemctl restart betanalysis && sleep 2 && sudo systemctl is-active betanalysis"
    ssh "$PI" "curl -s -o /dev/null -w 'dashboard: HTTP %{http_code}\n' http://localhost:8090/healthz"
fi

echo "== Deployed =="
