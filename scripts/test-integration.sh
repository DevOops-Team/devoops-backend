#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOMODCACHE="$PWD/.cache/mod" GOCACHE="$PWD/.cache/go"
if [[ -z "${TEST_MONGO_URI:-}" ]]; then
  docker compose -f compose.test.yaml up -d --wait
  docker compose -f compose.test.yaml exec -T mongo mongosh --quiet --eval 'try { rs.status() } catch(e) { rs.initiate({_id:"rs0",members:[{_id:0,host:"localhost:27017"}]}) }' >/dev/null
  export TEST_MONGO_URI='mongodb://127.0.0.1:27018/?replicaSet=rs0&directConnection=true'
fi
go test -race -count=1 ./...
