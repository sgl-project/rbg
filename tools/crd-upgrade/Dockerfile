# CRD Upgrader image for rbgs
# This image contains all CRD files and kubectl to upgrade CRDs during Helm upgrade
FROM alpine:3.20

# Copy CRD files and upgrade script
COPY ./config/crd/bases /rbgs/crds
COPY ./tools/crd-upgrade/ensure-crds-up-to-date.sh /rbgs/ensure-crds-up-to-date.sh

# Install necessary tools
RUN apk add --update --no-cache bash curl && \
    chmod +x /rbgs/ensure-crds-up-to-date.sh

# Install kubectl (supports multi-arch via TARGETARCH)
ARG TARGETARCH
RUN curl -L -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" && \
    chmod +x /usr/local/bin/kubectl

ENTRYPOINT ["bash", "/rbgs/ensure-crds-up-to-date.sh"]
