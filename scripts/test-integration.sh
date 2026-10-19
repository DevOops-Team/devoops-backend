#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOMODCACHE="$PWD/.cache/mod" GOCACHE="$PWD/.cache/go"
container=""
cleanup() {
  if [[ -n "$container" ]]; then
    docker rm -f -v "$container" >/dev/null
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -z "${TEST_MONGO_URI:-}" ]]; then
  # Docker-outside-of-Docker: share the agent's network namespace so Go on
  # the agent can reach MongoDB via localhost without host port forwarding.
  agent="${MONGO_TEST_AGENT_CONTAINER:-${HOSTNAME:-}}"
  port=27017
  network_args=()
  bind=127.0.0.1
  if [[ -n "$agent" ]] && docker inspect --type container "$agent" >/dev/null 2>&1; then
    port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
    network_args=(--network "container:$agent")
  else
    network_args=(-p 127.0.0.1::27017)
    bind=0.0.0.0
  fi
  # Capture the ID before starting, allowing cleanup even if startup fails.
  container=$(docker create "${network_args[@]}" mongo:8.0 \
    mongod --replSet rs0 --bind_ip "$bind" --port "$port")
  docker start "$container" >/dev/null
  ready=false
  for ((attempt=0; attempt<60; attempt++)); do
    if docker exec "$container" mongosh --port "$port" --quiet --eval \
      'try { rs.status() } catch(e) { rs.initiate({_id:"rs0",members:[{_id:0,host:"localhost:'"$port"'"}]}) }; quit(db.adminCommand({hello:1}).isWritablePrimary ? 0 : 1)' >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then
    docker logs --tail 50 "$container" >&2
    echo 'Temporary MongoDB did not become primary.' >&2
    exit 1
  fi
  if [[ "${network_args[0]}" == -p ]]; then
    port=$(docker port "$container" 27017/tcp | sed -n 's/^127\.0\.0\.1://p')
    [[ "$port" =~ ^[0-9]+$ ]]
  fi
  export TEST_MONGO_URI="mongodb://127.0.0.1:$port/?replicaSet=rs0&directConnection=true&serverSelectionTimeoutMS=5000"
fi
# Unit tests run in the preceding CI stage. Keep integration cases fresh and
# bound DB load to two parallel fixtures; retain the race detector.
go test -race -count=1 -tags integration -run '^TestIntegration' -parallel=2 ./internal/vdi
