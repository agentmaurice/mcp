#!/bin/sh
# Installs the disposable WordPress started by docker/compose.yaml with wp-cli,
# turns on pretty permalinks (needed for /wp-json) and creates an Application
# Password. It prints the three variables of the integration test.
#
#   docker compose -f docker/compose.yaml up -d
#   eval "$(sh docker/wp-setup.sh)"
#   go test -race -count=1 -run Integration -v ./internal/mcpserver
#
# Everything that is not the `export` lines goes to stderr, so `eval` is safe.
set -eu

HERE="$(cd "$(dirname "$0")" && pwd)"
COMPOSE="docker compose -f ${HERE}/compose.yaml"
SITE_URL="${CMS_WP_SITE_URL:-http://localhost:8080}"
ADMIN_USER="${CMS_WP_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${CMS_WP_ADMIN_PASSWORD:-admin-it-only-$$}"

wp() { ${COMPOSE} --profile tools run --rm -T wpcli wp "$@"; }

echo "waiting for the WordPress files (wp-config.php)..." >&2
i=0
until ${COMPOSE} exec -T wordpress test -f /var/www/html/wp-config.php >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -le 60 ] || { echo "wordpress container never became ready" >&2; exit 1; }
  sleep 2
done

if ! wp core is-installed >/dev/null 2>&1; then
  wp core install --url="${SITE_URL}" --title="CMS MCP integration" \
    --admin_user="${ADMIN_USER}" --admin_password="${ADMIN_PASSWORD}" \
    --admin_email="admin@example.test" --skip-email >&2
fi
wp rewrite structure '/%postname%/' --hard >&2
wp option update blogdescription "disposable test site" >&2

# A fresh Application Password per run (the value is shown only once).
APP_PASSWORD="$(wp user application-password create "${ADMIN_USER}" "cms-mcp-it-$(date +%s)" --porcelain)"

printf 'export CMS_WP_INTEGRATION_URL=%s\n' "${SITE_URL}"
printf 'export CMS_WP_INTEGRATION_USER=%s\n' "${ADMIN_USER}"
printf "export CMS_WP_INTEGRATION_APP_PASSWORD='%s'\n" "${APP_PASSWORD}"
