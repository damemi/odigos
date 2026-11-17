#!/usr/bin/env bash
#
# Reset the Odigos Insights service and its ClickHouse database from scratch.
#
# It drops every table in the insights database (and autopilot, when that
# schema is present), re-applies the schema migrations straight from source
# (internal/db/clickhouse/migrate/migrations and .../autopilot),
# records the applied version so the Helm migrate Job stays a no-op, and rolls
# the service. Nothing survives: baselines, observations, anomalies,
# transactions and the seeded policies are all recreated fresh.
#
# Migrations are applied with clickhouse-client (a real SQL parser), so this
# works even against an image whose embedded golang-migrate runner is broken
# (that runner splits on ';' naively and chokes on some comments).
#
# Usage:
#   insights/scripts/reset-db.sh
#
# Overridable via env:
#   NS               k8s namespace                (default: odigos-system)
#   INSIGHTS_DEPLOY  insights deployment name     (default: odigos-insights)
#   CH_DEPLOY        clickhouse deployment name   (default: odigos-insights-clickhouse)
#   DB               clickhouse database/user     (default: odigos_insights)
#   AUTOPILOT_DB autopilot database       (default: autopilot)
set -euo pipefail

NS="${NS:-odigos-system}"
INSIGHTS_DEPLOY="${INSIGHTS_DEPLOY:-odigos-insights}"
CH_DEPLOY="${CH_DEPLOY:-odigos-insights-clickhouse}"
DB="${DB:-odigos_insights}"
AUTOPILOT_DB="${AUTOPILOT_DB:-autopilot}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="$SCRIPT_DIR/../internal/db/clickhouse/migrate/migrations"
AUTOPILOT_MIGRATIONS_DIR="$SCRIPT_DIR/../internal/db/clickhouse/migrate/autopilot"

# Run whatever SQL arrives on stdin inside the ClickHouse pod. The password is
# read from the pod's own env so it never lands in shell history or argv.
# Optional first arg selects the database (defaults to $DB).
ch() {
	local db="${1:-$DB}"
	kubectl exec -i -n "$NS" "deploy/$CH_DEPLOY" -- sh -c \
		"clickhouse-client --user '$DB' --password \"\$CLICKHOUSE_PASSWORD\" --database '$db' --multiquery"
}

reset_database() {
	local db="$1"
	local migrations_dir="$2"
	echo "   - create database $db"
	printf 'CREATE DATABASE IF NOT EXISTS `%s`' "$db" | ch "$DB"
	echo "   - drop tables in $db"
	tables="$(printf 'SELECT name FROM system.tables WHERE database = currentDatabase()' | ch "$db" || true)"
	for t in $tables; do
		echo "     drop $t"
		printf 'DROP TABLE IF EXISTS `%s`' "$t" | ch "$db"
	done
	echo "   - apply migrations in $db"
	for f in "$migrations_dir"/*.up.sql; do
		echo "     apply $(basename "$f")"
		ch "$db" <"$f"
	done
	local latest seq
	latest="$(for f in "$migrations_dir"/*.up.sql; do basename "$f"; done | sed -E 's/^0*([0-9]+)_.*/\1/' | sort -n | tail -1)"
	seq="$(( $(date +%s) * 1000000000 ))" # portable ns-ish monotonic value (macOS date lacks %N)
	printf 'CREATE TABLE IF NOT EXISTS schema_migrations (version Int64, dirty UInt8, sequence UInt64) ENGINE = TinyLog;
TRUNCATE TABLE schema_migrations;
INSERT INTO schema_migrations (version, dirty, sequence) VALUES (%s, 0, %s);
' "$latest" "$seq" | ch "$db"
	echo "     marked version $latest, dirty 0"
}

echo ">> [1/4] Scaling $INSIGHTS_DEPLOY to 0 so nothing writes during the reset"
was="$(kubectl get deploy "$INSIGHTS_DEPLOY" -n "$NS" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo 1)"
{ [ -z "$was" ] || [ "$was" -lt 1 ] 2>/dev/null; } && was=1
kubectl scale deploy "$INSIGHTS_DEPLOY" -n "$NS" --replicas=0 >/dev/null
kubectl rollout status deploy/"$INSIGHTS_DEPLOY" -n "$NS" --timeout=90s >/dev/null

echo ">> [2/4] Resetting '$DB'"
reset_database "$DB" "$MIGRATIONS_DIR"

echo ">> [3/4] Resetting '$AUTOPILOT_DB'"
reset_database "$AUTOPILOT_DB" "$AUTOPILOT_MIGRATIONS_DIR"

echo ">> [4/4] Restoring $INSIGHTS_DEPLOY to $was replica(s)"
kubectl scale deploy "$INSIGHTS_DEPLOY" -n "$NS" --replicas="$was" >/dev/null
kubectl rollout status deploy/"$INSIGHTS_DEPLOY" -n "$NS" --timeout=120s

echo ">> Fresh table row counts:"
printf "SELECT database, name, total_rows FROM system.tables WHERE database IN ('%s', '%s') ORDER BY database, name" "$DB" "$AUTOPILOT_DB" | ch "$DB"

echo ">> Done. Insights + autopilot databases reset from scratch."
