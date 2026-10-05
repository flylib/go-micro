# OpenResty gateway image. Everything it needs is in this directory
# (vendored Lua libraries included), so the build needs no network.
ARG BASE=openresty/openresty:alpine
FROM ${BASE}

COPY lib /opt/micro/lib
COPY vendor/lua /opt/micro/vendor/lua
COPY conf/nginx.conf.tmpl /opt/micro/conf/nginx.conf.tmpl
COPY docker-entrypoint.sh /opt/micro/docker-entrypoint.sh
# run unprivileged: master and workers share one user, which also lets a
# worker stop the gateway when the rules cannot be loaded (SPEC 10)
RUN mkdir -p /opt/micro/logs && chmod +x /opt/micro/docker-entrypoint.sh \
    && chown -R nobody:nobody /opt/micro
USER nobody

EXPOSE 8080
ENTRYPOINT ["/opt/micro/docker-entrypoint.sh"]
