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

sed -e "s|__LISTEN__|$listen|" \
    -e "s|__RESOLVER__|$resolver|" \
    -e "s|__SCHEME__|$scheme|" \
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
