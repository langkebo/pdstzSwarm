#!/usr/bin/env bash
# gen-ssl.sh — generate a self-signed TLS cert + key for
# the Pentest Swarm AI API server.
#
# Usage:
#   ./deploy/scripts/gen-ssl.sh                      # default CN=pentestswarm.local
#   ./deploy/scripts/gen-ssl.sh 192.168.206.100      # add IP to SAN
#   ./deploy/scripts/gen-ssl.sh my.host.example.com  # add DNS to SAN
#
# The script writes to deploy/ssl/ (which is the directory
# docker-compose bind-mounts into /etc/pentestswarm/ssl/).
# Re-running is idempotent: existing files are overwritten.
#
# For production swap in Let's Encrypt via certbot and drop
# the resulting fullchain.pem + privkey.pem into the same
# directory; only the file names differ (server.crt ←→
# fullchain.pem, server.key ←→ privkey.pem) so either set
# SERVER_SSL_CRT/KEY accordingly or rename.

set -euo pipefail

# Resolve the repo root regardless of where the script is
# invoked from. The script lives at deploy/scripts/gen-ssl.sh
# so two dirname calls land us at the project root.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SSL_DIR="${REPO_ROOT}/deploy/ssl"

mkdir -p "${SSL_DIR}"
chmod 700 "${SSL_DIR}"

CERT="${SSL_DIR}/server.crt"
KEY="${SSL_DIR}/server.key"

# Build the SAN list. The default covers the most common
# dev paths (localhost / 127.0.0.1) and any extra positional
# args are appended verbatim so operators can throw in a LAN
# IP or a hostname.
SAN_ENTRIES="DNS:localhost,DNS:pentestswarm.local,IP:127.0.0.1,IP:::1"
for extra in "$@"; do
    if [[ "${extra}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        SAN_ENTRIES="${SAN_ENTRIES},IP:${extra}"
    else
        SAN_ENTRIES="${SAN_ENTRIES},DNS:${extra}"
    fi
done

echo ">> generating self-signed cert"
echo "   cert:  ${CERT}"
echo "   key:   ${KEY}"
echo "   SAN:   ${SAN_ENTRIES}"

# -nodes  : no passphrase on the key (so the binary can
#           read it without a TTY)
# -x509   : self-signed root, no CSR round-trip
# -sha256 : modern digest (browsers reject SHA-1 in 2026+)
# -addext : embed SANs directly so modern browsers accept
#           the cert even without a real CN match
openssl req -x509 -newkey rsa:4096 -nodes -sha256 -days 365 \
    -keyout "${KEY}" \
    -out    "${CERT}" \
    -subj   "/CN=pentestswarm.local" \
    -addext "subjectAltName=${SAN_ENTRIES}" \
    2>&1 | sed 's/^/   /'

# Restrict the key to owner-only (0600). The container's
# bind-mount preserves the host perms, so this is the
# single moment we get to lock it down. The boot path
# warns (loudly) if it ever sees a 0644 key in the wild.
chmod 600 "${KEY}"
chmod 644 "${CERT}"

echo ""
echo ">> done. To enable HTTPS:"
echo "   1. set in .env:"
echo "        SERVER_USE_SSL=true"
echo "        SERVER_SSL_CRT=/etc/pentestswarm/ssl/server.crt"
echo "        SERVER_SSL_KEY=/etc/pentestswarm/ssl/server.key"
echo "   2. docker compose -f deploy/docker-compose.yml up -d"
echo "   3. visit https://localhost:8081/  (browsers will warn"
echo "      about the self-signed cert; click 'Advanced' →"
echo "      'Proceed' to continue, or add the cert to your"
echo "      OS trust store for a clean experience)"
