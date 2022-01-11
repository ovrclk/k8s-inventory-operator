LABEL org.opencontainers.image.source https://github.com/ovrclk/k8s-inventory-operator

FROM alpine

COPY inventory /bin/

RUN apk add --no-cache tini

ENTRYPOINT ["/sbin/tini", "--", "/bin/inventory" ]
