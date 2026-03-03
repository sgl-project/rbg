# CRD Upgrader image for rbgs
# This image contains all CRD files and kubectl to upgrade CRDs during Helm upgrade
FROM alpine:3.20

# Copy CRD files and upgrade script
COPY ./deploy/helm/rbgs/crds /rbgs/crds
COPY ./tools/crd-upgrade/upgrade-crds.sh /rbgs/upgrade-crds.sh

# Install necessary tools
RUN apk add --update --no-cache bash curl && \
    chmod +x /rbgs/upgrade-crds.sh

# Install kubectl (supports multi-arch via TARGETARCH)
ARG TARGETARCH
RUN curl -L -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" && \
    chmod +x /usr/local/bin/kubectl

ENTRYPOINT ["bash", "/rbgs/upgrade-crds.sh"]
