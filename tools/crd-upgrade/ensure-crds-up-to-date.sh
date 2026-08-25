#!/bin/bash
set -e

# CRDs that require a conversion webhook clientConfig.
# After install/replace, spec.conversion is patched with the correct service
# name and namespace so Kubernetes routes conversion requests to the webhook.
CONVERSION_CRDS=(
  "rolebasedgroups.workloads.x-k8s.io"
  "rolebasedgroupsets.workloads.x-k8s.io"
)

# Namespace and service name for the conversion webhook.
# Override via environment variables when invoking this script.
WEBHOOK_NAMESPACE="${WEBHOOK_NAMESPACE:-rbgs-system}"
WEBHOOK_SERVICE="${WEBHOOK_SERVICE:-rbgs-webhook-service}"

while IFS= read -r -d '' crdfile; do
  cat "$crdfile"
  crdshort=${crdfile#*_}
  if [[ $(kubectl get --ignore-not-found -f "$crdfile" | wc -l) -gt 0 ]]; then
    echo "$crdshort found, replacing its crd..."
    kubectl replace -f "$crdfile"
  else
    echo "$crdshort not found, creating its crd..."
    kubectl create -f "$crdfile"
  fi
done < <(find /rbgs/crds -maxdepth 1 -name '*.yaml' -print0)

# Patch spec.conversion on CRDs that use the conversion webhook.
# The base CRD files (from controller-gen) do not include spec.conversion;
# it must be applied separately, mirroring what Kustomize does via
# config/crd/patches/webhook_in_*.yaml.
CONVERSION_PATCH=$(cat <<EOF
{"spec":{"conversion":{"strategy":"Webhook","webhook":{"conversionReviewVersions":["v1"],"clientConfig":{"service":{"namespace":"${WEBHOOK_NAMESPACE}","name":"${WEBHOOK_SERVICE}","path":"/convert"}}}}}}
EOF
)

for crd in "${CONVERSION_CRDS[@]}"; do
  echo "Patching conversion webhook on CRD: $crd"
  kubectl patch crd "$crd" --type=merge -p "${CONVERSION_PATCH}"
done
