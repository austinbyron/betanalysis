#!/usr/bin/env bash
# One-time Pi bootstrap for betanalysis. Run as root:
#   sudo bash /home/homelab/betanalysis/bootstrap-pi.sh
#
# Everything here is idempotent — safe to re-run.
set -euo pipefail

# iTerm2 forwards the Mac's LANG/LC_* over SSH, but en_US.UTF-8 isn't
# generated on the Pi — initdb refuses an invalid locale. Pin C.UTF-8,
# which always exists on Debian.
export LC_ALL=C.UTF-8 LANG=C.UTF-8
unset LANGUAGE LC_TERMINAL LC_TERMINAL_VERSION 2>/dev/null || true

APP_DIR=/home/homelab/betanalysis
PGDATA_SSD=/mnt/ssd/postgresql

echo "== 1/6 PostgreSQL install =="
if ! command -v psql >/dev/null; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql
fi
# Detect from installed binaries, not /etc/postgresql — a dropped cluster
# removes its /etc config dir and would leave this empty.
PG_VERSION=$(ls /usr/lib/postgresql | sort -n | tail -1)
if [ -z "$PG_VERSION" ]; then
    echo "no postgresql version found under /usr/lib/postgresql" >&2
    exit 1
fi
echo "postgres version: $PG_VERSION"

echo "== 2/6 Data directory on the SSD =="
CURRENT_DATADIR=$(pg_lsclusters -h | awk -v v="$PG_VERSION" '$1==v && $2=="main" {print $6}')
if [ "$CURRENT_DATADIR" != "$PGDATA_SSD/$PG_VERSION/main" ]; then
    echo "moving cluster data dir to the SSD (was: ${CURRENT_DATADIR:-none})"
    if [ -n "$CURRENT_DATADIR" ]; then
        pg_dropcluster --stop "$PG_VERSION" main
    fi
    mkdir -p "$PGDATA_SSD"
    chown postgres:postgres "$PGDATA_SSD"
    pg_createcluster --locale C.UTF-8 -d "$PGDATA_SSD/$PG_VERSION/main" "$PG_VERSION" main
fi
systemctl enable --now postgresql

echo "== 3/6 Database and role =="
sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='betuser'" | grep -q 1 || \
    sudo -u postgres psql -c "CREATE ROLE betuser LOGIN PASSWORD 'betpass'"
sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='betanalysis'" | grep -q 1 || \
    sudo -u postgres createdb -O betuser betanalysis

echo "== 4/6 Migrations =="
for migration in "$APP_DIR"/migrations/*.sql; do
    echo "applying $(basename "$migration")"
    PGPASSWORD=betpass psql -h 127.0.0.1 -U betuser -d betanalysis -q -f "$migration"
done

echo "== 5/6 systemd unit =="
cp "$APP_DIR/betanalysis.service" /etc/systemd/system/betanalysis.service
systemctl daemon-reload
systemctl enable --now betanalysis

echo "== 6/6 Caddy route + scoped deploy sudoers =="
if ! grep -q "betanalysis.homelab" /etc/caddy/Caddyfile; then
    printf '\nbetanalysis.homelab:80 {\n    reverse_proxy localhost:8090\n}\n' >> /etc/caddy/Caddyfile
    systemctl reload caddy
fi

# Passwordless deploys from here on, scoped to exactly these commands
# (printf|tee, not a heredoc — heredocs through ssh -t corrupt sudoers)
printf '%s\n' \
    'homelab ALL=(root) NOPASSWD: /usr/bin/systemctl restart betanalysis, /usr/bin/systemctl stop betanalysis, /usr/bin/systemctl start betanalysis, /usr/bin/systemctl status betanalysis, /usr/bin/systemctl is-active betanalysis, /usr/bin/systemctl reload caddy' \
    'homelab ALL=(root) NOPASSWD: /usr/bin/journalctl -u betanalysis *' \
    | tee /etc/sudoers.d/betanalysis-deploy >/dev/null
chmod 440 /etc/sudoers.d/betanalysis-deploy
visudo -cf /etc/sudoers.d/betanalysis-deploy

echo
echo "== Done. Checks =="
systemctl is-active postgresql betanalysis caddy
curl -s -o /dev/null -w "dashboard: HTTP %{http_code}\n" http://localhost:8090/healthz
