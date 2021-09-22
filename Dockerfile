FROM alpine
ARG TINY_VERSION=v0.19.0

COPY ./inventory /bin/

RUN apk add --no-cache tini

ENTRYPOINT ["/sbin/tini", "--", "/bin/inventory" ]
