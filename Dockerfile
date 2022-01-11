FROM alpine
LABEL "org.opencontainers.image.source"="https://github.com/ovrclk/k8s-inventory-operator"

COPY inventory /bin/

RUN apk add --no-cache tini

ENTRYPOINT ["/sbin/tini", "--", "/bin/inventory" ]
