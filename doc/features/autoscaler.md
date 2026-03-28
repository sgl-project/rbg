# Autoscaling

RBG scaling provides a scalingAdapter that supports HPA, KEDA, or KPA to adjust the number of replicas for Roles.

The RBGSA exposes `status.readyReplicas` mirrored from the parent RBG's `status.roleStatuses[].readyReplicas`, allowing consumers to read readiness directly without a cross-resource lookup.

![autoscaler](../img/autoscaler.jpg)
