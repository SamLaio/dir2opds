#!/usr/bin/env bash
set -euo pipefail

image=${1:-dir2opds:ci}
books=$(mktemp -d)
container=
cleanup() {
  if [[ -n "$container" ]]; then
    docker logs "$container"
    docker rm -f "$container" >/dev/null
  fi
  rm -rf -- "$books"
}
trap cleanup EXIT
printf "container-download-check" > "$books/public.txt"
container=$(docker run -d --user "$(id -u):$(id -g)" -p 127.0.0.1::8080 -v "$books:/books" "$image" -dir /books)
port=$(docker port "$container" 8080/tcp)
base="http://$port"
health=$(curl -fsS --retry 20 --retry-connrefused --retry-delay 1 --max-time 2 "$base/health")
[[ "$health" == '{"status":"ok"}' ]]
[[ "$(curl -fsS --max-time 5 "$base/public.txt")" == "container-download-check" ]]
printf "Container health and file download passed\n"
