/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workloads

import (
	"context"
	"time"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	rbgwebhook "sigs.k8s.io/rbgs/pkg/webhook"
)

// WebhookCertReconciler watches the conversion-webhook CRDs and keeps their
// caBundle patched with the current CA certificate.
// It also re-patches on a fixed interval to recover from out-of-band changes.
type WebhookCertReconciler struct {
	client.Client
	CertManager *rbgwebhook.CertManager
	CACert      []byte
	// CRDNames is the list of CRD names whose caBundle should be kept in sync.
	CRDNames []string
}

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;patch
// Secret access is intentionally namespace-scoped (Role, not ClusterRole) and is managed
// manually in config/rbac/secret_role.yaml rather than generated from markers below,
// because kubebuilder markers do not support resourceNames scoping.
// The controller only needs: create secrets (any name, to bootstrap), get+update rbgs-webhook-cert.

func (r *WebhookCertReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithName("webhook-cert-reconciler")

	if err := r.CertManager.PatchCRDCABundle(ctx, r.CRDNames, r.CACert); err != nil {
		log.Error(err, "failed to patch caBundle on CRDs")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Re-check periodically in case the CRD is replaced or the caBundle is removed.
	return reconcile.Result{RequeueAfter: 10 * time.Minute}, nil
}

// SetupWithManager registers the reconciler to watch the conversion-webhook CRDs.
func (r *WebhookCertReconciler) SetupWithManager(mgr ctrl.Manager, opts controller.Options) error {
	// Only react to the specific CRDs we care about.
	crdNameSet := make(map[string]bool, len(r.CRDNames))
	for _, n := range r.CRDNames {
		crdNameSet[n] = true
	}
	nameFilter := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return crdNameSet[e.Object.GetName()]
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return crdNameSet[e.ObjectNew.GetName()]
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // CRDs are not deleted in normal operation
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return crdNameSet[e.Object.GetName()]
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(opts).
		For(&apiextv1.CustomResourceDefinition{}, builder.WithPredicates(nameFilter)).
		Complete(r)
}

// EnqueueCRDs returns a list of reconcile.Request for all watched CRDs,
// used to trigger an initial reconciliation at startup.
func (r *WebhookCertReconciler) EnqueueCRDs() []reconcile.Request {
	reqs := make([]reconcile.Request, 0, len(r.CRDNames))
	for _, name := range r.CRDNames {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKey{Name: name},
		})
	}
	return reqs
}
