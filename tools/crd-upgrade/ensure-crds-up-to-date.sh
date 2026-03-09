#!/bin/bash
set -e

for crdfile in $(find /rbgs/crds/*.yaml);
do
  crdshort=${crdfile#*_}
  if [[ $(kubectl get --ignore-not-found -f $crdfile | wc -l) -gt 0 ]]; then
    echo "$crdshort found, replacing its crd..."
    kubectl replace -f $crdfile
  else
    echo "$crdshort not found, creating its crd..."
    kubectl create -f $crdfile
  fi
done
