#!/bin/sh
# Runs the Lua unit tests in an OpenResty container. Files are copied in
# rather than bind-mounted, so this also works where the project path is
# not shared with the docker VM.
set -eu
cd "$(dirname "$0")/.."

if ! cmp -s ../rules.schema.json lib/resty/micro/schema.json; then
  echo "lib/resty/micro/schema.json differs from gateway/rules.schema.json:"
  echo "  cp ../rules.schema.json lib/resty/micro/schema.json"
  exit 1
fi

name="micro-openresty-test-$$"
docker create --name "$name" --entrypoint openresty "${BASE:-openresty/openresty:alpine}" \
  -p /t -e stderr -c /t/t/runner.conf >/dev/null
trap 'docker rm -f "$name" >/dev/null' EXIT
docker cp . "$name:/t" >/dev/null
docker cp ../rules.example.yaml "$name:/t/t/rules.example.yaml" >/dev/null
docker start -a "$name"
