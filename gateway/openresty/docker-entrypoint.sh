#!/bin/sh
# Renders conf/nginx.conf from the bootstrap settings nginx cannot read
# from the environment itself (SPEC 11), then runs OpenResty. Everything
# else is read by Lua at init.
set -eu

addr="${MICRO_GATEWAY_ADDRESS:-:8080}"
case "$addr" in
  :*) listen="${addr#:}" ;; # Go-style ":8080" -> nginx "8080"
  *) listen="$addr" ;;
esac

resolver=$(awk '/^nameserver/ { print $2; exit }' /etc/resolv.conf 2>/dev/null || true)
[ -n "$resolver" ] || resolver=127.0.0.11

scheme=grpc
[ "${MICRO_GATEWAY_UPSTREAM_TLS:-false}" = "true" ] && scheme=grpcs

# optional HTTP/JSON entry (SPEC 2.1): drop its server block when unset
http_listen=""
case "${MICRO_GATEWAY_HTTP_ADDRESS:-}" in
  "") ;;
  :*) http_listen="${MICRO_GATEWAY_HTTP_ADDRESS#:}" ;;
  *) http_listen="$MICRO_GATEWAY_HTTP_ADDRESS" ;;
esac
http_block=""
[ -n "$http_listen" ] || http_block='/# BEGIN HTTP\/JSON entry/,/# END HTTP\/JSON entry/d'

# optional WebSocket forwarding to a Go gateway (SPEC 2.3): without it,
# /ws is an unknown path of the HTTP entry (404)
ws_upstream="${MICRO_GATEWAY_WS_UPSTREAM:-}"
ws_block=""
[ -n "$ws_upstream" ] || ws_block='/# BEGIN WebSocket forwarding/,/# END WebSocket forwarding/d'

sed -e "s|__LISTEN__|$listen|" \
    -e "s|__HTTP_LISTEN__|$http_listen|" \
    -e "s|__RESOLVER__|$resolver|" \
    -e "s|__SCHEME__|$scheme|" \
    -e "s|__WS_UPSTREAM__|$ws_upstream|" \
    ${http_block:+-e "$http_block"} \
    ${ws_block:+-e "$ws_block"} \
    /opt/micro/conf/nginx.conf.tmpl > /opt/micro/conf/nginx.conf

# Not exec'd: when the rules cannot be loaded at start-up a worker stops
# nginx and leaves a marker, and the container must then exit non-zero.
rm -f /tmp/micro-gateway-failed
openresty -p /opt/micro -c conf/nginx.conf -g 'daemon off;' &
pid=$!
trap 'kill -TERM $pid' TERM INT
trap 'kill -QUIT $pid' QUIT
status=0
while :; do
  # wait returns early when a trapped signal arrives; loop until nginx
  # itself has exited, then keep its exit status
  wait "$pid" && status=0 || status=$?
  kill -0 "$pid" 2>/dev/null || break
done
[ -f /tmp/micro-gateway-failed ] && exit 1
exit "$status"
