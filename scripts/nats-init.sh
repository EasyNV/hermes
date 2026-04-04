#!/bin/sh
# Create all NATS JetStream streams in the correct order.
# Must run BEFORE any Go service starts.
#
# The key insight: HERMES_WA uses "hermes.wa.>" which is a superset of
# the campaign and manual send subjects. We need to either:
# a) Use a single broad stream (HERMES_WA) and subscribe from it, OR
# b) Create narrower streams FIRST, then the broad one excludes those subjects.
#
# NATS JetStream does NOT allow subject overlap between streams.
# Solution: HERMES_WA covers hermes.wa.message.* + hermes.wa.ban.* + hermes.wa.connection.* + hermes.wa.presence.*
# HERMES_CAMPAIGN covers hermes.campaign.* + hermes.wa.send.campaign.*
# HERMES_INBOX covers hermes.wa.send.manual.*

set -e

NATS_URL="${NATS_URL:-nats://nats:4222}"

echo "Waiting for NATS..."
until nats stream ls --server="$NATS_URL" >/dev/null 2>&1; do
  sleep 1
done
echo "NATS ready."

# Create streams with non-overlapping subjects.
# Order matters: create specific subjects first.

nats stream add HERMES_CAMPAIGN \
  --server="$NATS_URL" \
  --subjects="hermes.campaign.>,hermes.wa.send.campaign.>" \
  --storage=file --retention=limits --max-age=720h --replicas=1 \
  --discard=old --no-deny-delete --no-deny-purge \
  --defaults 2>/dev/null || echo "HERMES_CAMPAIGN already exists"

nats stream add HERMES_INBOX \
  --server="$NATS_URL" \
  --subjects="hermes.wa.send.manual.>" \
  --storage=file --retention=limits --max-age=24h --replicas=1 \
  --discard=old --no-deny-delete --no-deny-purge \
  --defaults 2>/dev/null || echo "HERMES_INBOX already exists"

nats stream add HERMES_WA \
  --server="$NATS_URL" \
  --subjects="hermes.wa.message.>,hermes.wa.ban.>,hermes.wa.connection.>,hermes.wa.presence.>" \
  --storage=file --retention=limits --max-age=168h --replicas=1 \
  --discard=old --no-deny-delete --no-deny-purge \
  --defaults 2>/dev/null || echo "HERMES_WA already exists"

nats stream add HERMES_CONTACTS \
  --server="$NATS_URL" \
  --subjects="hermes.contacts.>" \
  --storage=file --retention=limits --max-age=24h --replicas=1 \
  --discard=old --no-deny-delete --no-deny-purge \
  --defaults 2>/dev/null || echo "HERMES_CONTACTS already exists"

nats stream add HERMES_NOTIFY \
  --server="$NATS_URL" \
  --subjects="hermes.notify.>" \
  --storage=file --retention=limits --max-age=1h --replicas=1 \
  --discard=old --no-deny-delete --no-deny-purge \
  --defaults 2>/dev/null || echo "HERMES_NOTIFY already exists"

echo "All NATS streams created."
nats stream ls --server="$NATS_URL"
