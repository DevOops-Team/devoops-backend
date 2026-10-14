#!/usr/bin/env bash
set -euo pipefail
set +x
cd "$(dirname "$0")/.."
: "${KUBECONFIG:?Provide the staging-only kubeconfig}"

# Keep the tunnel and Go tests in the same Jenkins agent/network namespace.
namespace=vdi-staging
forward_log=$(mktemp)
forward_pid=''
cleanup() {
  if [[ -n "$forward_pid" ]]; then
    kill "$forward_pid" 2>/dev/null || true
    wait "$forward_pid" 2>/dev/null || true
  fi
  rm -f "$forward_log"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# An ephemeral local port avoids collisions with other CI jobs and local MongoDB.
kubectl --kubeconfig "$KUBECONFIG" -n "$namespace" port-forward \
  --address 127.0.0.1 svc/mongodb :27017 >"$forward_log" 2>&1 &
forward_pid=$!

ready=false
for ((attempt = 0; attempt < 60; attempt++)); do
  if ! kill -0 "$forward_pid" 2>/dev/null; then
    cat "$forward_log" >&2
    echo 'MongoDB port-forward stopped before tests started.' >&2
    exit 1
  fi
  port=$(sed -nE 's/^Forwarding from 127[.]0[.]0[.]1:([0-9]+) -> 27017$/\1/p' "$forward_log" | head -n 1)
  if [[ -n "$port" ]]; then
    export TEST_MONGO_URI="mongodb://127.0.0.1:$port/?replicaSet=rs0&directConnection=true&serverSelectionTimeoutMS=2000&connectTimeoutMS=2000"
    if mongosh "$TEST_MONGO_URI" --quiet --eval \
      'quit(db.adminCommand({hello: 1}).isWritablePrimary ? 0 : 1)' >/dev/null 2>&1; then
      ready=true
      break
    fi
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  cat "$forward_log" >&2
  echo 'Staging MongoDB did not become a writable replica-set primary.' >&2
  exit 1
fi

# TEST_MONGO_URI bypasses the Docker Compose database in test-integration.sh.
make test-integration
