# Labels, Annotations and Environment Variables

## Labels

 Key                                                                | Description
--------------------------------------------------------------------|-------------------------------------------------------------------------
 rolebasedgroup.workloads.x-k8s.io/name                             | The name of the RoleBasedGroup to which these resources belong.
 rolebasedgroup.workloads.x-k8s.io/role                             | The name of the role to which these resources belong.
 pod-group.scheduling.sigs.k8s.io/name                              | The name of the podGroup for gang scheduling.
 rolebasedgroup.workloads.x-k8s.io/group-unique-key                 | The unique key of the group. Used as match label for topology affinity.
 rolebasedgroup.workloads.x-k8s.io/controller-revision-hash         | The version hash corresponding to the RBG object, used to determine whether the RBG object has changed.
 rolebasedgroup.workloads.x-k8s.io/role-revision-hash-\<role-name\> | The version hash corresponding to the specific RBG role, used to determine whether the current role has changed.

## Annotations

 Key                                                          | Description
--------------------------------------------------------------|------------------------------------------------------------------------------------------------------
 rolebasedgroup.workloads.x-k8s.io/role-size                  | The size of the role.
 rolebasedgroup.workloads.x-k8s.io/exclusive-topology         | Declares the topology domain (e.g. kubernetes.io/hostname) for exclusive scheduling.
 rolebasedgroup.workloads.x-k8s.io/disable-exclusive-topology | Can be set to "true" on a Role template to skip exclusive-topology affinity injection for that role.

## Env Variables

 Key        | Description
------------|----------------------------------------------------
 GROUP_NAME | The name of the RoleBasedGroup.
 ROLE_NAME  | The name of the role.
 ROLE_INDEX | The index or identity of the pod within the role.  
