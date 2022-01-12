FROM debian
LABEL "org.opencontainers.image.source"="https://github.com/ovrclk/k8s-inventory-operator"

COPY inventory /bin/

RUN \
    apt-get update \
 && apt-get install -y --no-install-recommends \
    tini \
 && rm -rf /var/lib/apt/lists/*

#RUN apk add --no-cache tini

ENTRYPOINT ["/usr/bin/tini", "--", "/bin/inventory" ]
