#!/bin/sh
# Creates the showcase databases of adaptive-pg-mcp on midPoint's Postgres
# server and fills them, as midPoint's superuser. It never opens midPoint's
# own database. Compose runs it again on every up, so each step checks first:
# a re-run makes only what is missing, such as a database dropped for the id
# check, and fills only the tables it creates. Each data file runs as one
# transaction, so a failed run leaves no half-filled database behind.
set -eu

export PGHOST=midpoint-db PGUSER=midpoint PGPASSWORD=midpoint PGOPTIONS="-c client_min_messages=warning"
psql_in() { # $1 database, then psql arguments
  db=$1
  shift
  psql -X -q -v ON_ERROR_STOP=1 -d "$db" "$@"
}

n=0
until pg_isready -q -d postgres; do
  n=$((n + 1))
  if [ "$n" -gt 60 ]; then
    echo "pg-showcase: midpoint-db did not accept connections within 120 seconds, so no showcase database was made. Read docker compose logs midpoint-db, then run this service again." >&2
    exit 1
  fi
  sleep 2
done

psql_in postgres -f /pg-showcase/init/cluster.sql
psql_in helpdesk -1 -f /pg-showcase/init/helpdesk.sql
psql_in sales_eu -1 -v seed=0.11 -v currency=EUR -v login=sales_eu_app \
  -v regions='{DE,FR,NL,AT,SK,CZ,PL,IT,ES}' -f /pg-showcase/init/sales.sql
psql_in sales_us -1 -v seed=0.22 -v currency=USD -v login=sales_us_app \
  -v regions='{CA,NY,TX,WA,IL,MA,FL,GA}' -f /pg-showcase/init/sales.sql
psql_in playground -1 -f /pg-showcase/init/playground.sql
echo "pg-showcase: helpdesk, sales_eu, sales_us and playground are ready"
