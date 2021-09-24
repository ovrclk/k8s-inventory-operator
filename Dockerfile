FROM alpine

COPY inventory /bin/

RUN apk add --no-cache tini

ENTRYPOINT ["/sbin/tini", "--", "/bin/inventory" ]
