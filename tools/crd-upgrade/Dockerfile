# CRD Upgrader image for rbgs
# This image contains all CRD files and kubectl to upgrade CRDs during Helm upgrade
FROM alpine:3.20

# Copy CRD files and upgrade script
COPY ./config/crd/bases /rbgs/crds
COPY ./tools/crd-upgrade/ensure-crds-up-to-date.sh /rbgs/ensure-crds-up-to-date.sh

# Install necessary tools
#
# Use chinese mirror:
# RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
#     apk add --update --no-cache bash curl && \
#     chmod +x /rbgs/ensure-crds-up-to-date.sh
RUN apk add --update --no-cache bash curl && \
    chmod +x /rbgs/ensure-crds-up-to-date.sh

# Install kubectl (use Aliyun mirror for CN network environment)
#
# Install via chinese mirror:
# RUN apk add --no-cache kubectl --repository=https://dl-cdn.alpinelinux.org/alpine/v3.20/community
ARG KUBECTL_VERSION=v1.31.0
ARG TARGETARCH=amd64
RUN curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" && \
    curl -LO "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256" && \
    echo "$(cat kubectl.sha256)  kubectl" | sha256sum -c && \
    mv kubectl /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl && \
    rm kubectl.sha256

ENTRYPOINT ["bash", "/rbgs/ensure-crds-up-to-date.sh"]
