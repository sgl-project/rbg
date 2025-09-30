FROM registry-cn-hangzhou.ack.aliyuncs.com/dev/alpine:3.18-update

COPY ./deploy/helm/rbg/crds /rbg/crds
COPY ./tools/crd-upgrade/upgrade-crds.sh /rbg/upgrade-crds.sh

RUN apk add --update bash curl iproute2 libc6-compat tzdata vim &&  \
 	rm -rf /var/cache/apk/*

# need kubectl as upgrade-crds.sh uses it.
ARG TARGETARCH
RUN curl -L -o /usr/local/bin/kubectl "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" && chmod +x /usr/local/bin/kubectl

ENTRYPOINT ["bash","/rbg/upgrade-crds.sh"]
