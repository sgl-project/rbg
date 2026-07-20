/*
Copyright 2026 The RBG Authors.

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

package v1alpha2

import (
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	rbgwebhook "sigs.k8s.io/rbgs/pkg/webhook"
	"sigs.k8s.io/rbgs/test/e2e/framework"
)

// RBAC object names produced by the rbgs Helm chart (deploy/helm/rbgs/templates/rbac)
// and the kustomize config (config/rbac). Kept in one place so a chart reorganization
// that drops or renames one of them fails these cases with a clear signal rather than
// an opaque downstream webhook-TLS error.
const (
	controllerClusterRoleName        = "rbgs-controller-role"
	controllerClusterRoleBindingName = "rbgs-controller-rolebinding"
	certRoleName                     = "rbgs-controller-secret-role"
	certRoleBindingName              = "rbgs-controller-secret-rolebinding"
	controllerServiceAccountName     = "rbgs-controller-sa"
)

const (
	bootstrapTimeout  = 2 * time.Minute
	bootstrapInterval = 5 * time.Second
)

// controllerNamespace is where the controller (and its RBAC + webhook cert secret) is
// deployed. Defaults to the chart's rbgs-system; override with RBGS_NAMESPACE.
func controllerNamespace() string {
	if ns := os.Getenv("RBGS_NAMESPACE"); ns != "" {
		return ns
	}
	return "rbgs-system"
}

// RunRBACAndWebhookBootstrapTestCases verifies, against the real cluster the chart was
// installed into, that the controller's RBAC wiring is present and that the webhook-cert
// bootstrap actually works end to end. The webhook-cert Role is hand-maintained
// (namespace- and resourceName-scoped, not marker-generated), so it is the piece most
// likely to break silently when the chart/kustomize layout is reorganized.
func RunRBACAndWebhookBootstrapTestCases(f *framework.Framework) {
	ginkgo.Describe(
		"controller RBAC and webhook-cert bootstrap", func() {
			ns := controllerNamespace()

			ginkgo.BeforeEach(func() {
				f.RegisterDebugFn(func() { dumpBootstrapDebugInfo(f, ns) })
			})

			ginkgo.It(
				"should install the controller RBAC objects with resolvable bindings", func() {
					_, err := f.Clientset.RbacV1().ClusterRoles().Get(f.Ctx, controllerClusterRoleName, metav1.GetOptions{})
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "ClusterRole %q should exist", controllerClusterRoleName)

					_, err = f.Clientset.CoreV1().ServiceAccounts(ns).Get(f.Ctx, controllerServiceAccountName, metav1.GetOptions{})
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "ServiceAccount %s/%s should exist", ns, controllerServiceAccountName)

					crb, err := f.Clientset.RbacV1().ClusterRoleBindings().Get(f.Ctx, controllerClusterRoleBindingName, metav1.GetOptions{})
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "ClusterRoleBinding %q should exist", controllerClusterRoleBindingName)
					gomega.Expect(crb.RoleRef.Kind).Should(gomega.Equal("ClusterRole"))
					gomega.Expect(crb.RoleRef.Name).Should(gomega.Equal(controllerClusterRoleName),
						"ClusterRoleBinding roleRef must resolve to the controller ClusterRole")
					gomega.Expect(subjectsHaveServiceAccount(crb.Subjects, controllerServiceAccountName, ns)).Should(gomega.BeTrue(),
						"ClusterRoleBinding must bind ServiceAccount %s/%s", ns, controllerServiceAccountName)

					rb, err := f.Clientset.RbacV1().RoleBindings(ns).Get(f.Ctx, certRoleBindingName, metav1.GetOptions{})
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "RoleBinding %s/%s should exist", ns, certRoleBindingName)
					gomega.Expect(rb.RoleRef.Kind).Should(gomega.Equal("Role"))
					gomega.Expect(rb.RoleRef.Name).Should(gomega.Equal(certRoleName),
						"cert RoleBinding roleRef must resolve to the webhook-cert Role")
					gomega.Expect(subjectsHaveServiceAccount(rb.Subjects, controllerServiceAccountName, ns)).Should(gomega.BeTrue(),
						"cert RoleBinding must bind ServiceAccount %s/%s", ns, controllerServiceAccountName)
				},
			)

			ginkgo.It(
				"should scope the webhook-cert Role to secret create + rbgs-webhook-cert get/update", func() {
					role, err := f.Clientset.RbacV1().Roles(ns).Get(f.Ctx, certRoleName, metav1.GetOptions{})
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "Role %s/%s should exist", ns, certRoleName)

					secret := rbgwebhook.WebhookCertSecretName
					gomega.Expect(secretVerbAllowed(role, "create", "")).Should(gomega.BeTrue(),
						"cert Role must allow creating secrets (to bootstrap %s)", secret)
					gomega.Expect(secretVerbAllowed(role, "get", secret)).Should(gomega.BeTrue(),
						"cert Role must allow get on secret %s", secret)
					gomega.Expect(secretVerbAllowed(role, "update", secret)).Should(gomega.BeTrue(),
						"cert Role must allow update on secret %s", secret)
				},
			)

			ginkgo.It(
				"should bootstrap the webhook-cert secret in the live cluster", func() {
					// This only succeeds if the controller could create+get+update the secret
					// through the cert Role above (no RBAC Forbidden), so it exercises the Role
					// for real rather than just reading its rules.
					secret := rbgwebhook.WebhookCertSecretName
					gomega.Eventually(func() error {
						s, err := f.Clientset.CoreV1().Secrets(ns).Get(f.Ctx, secret, metav1.GetOptions{})
						if err != nil {
							return err
						}
						if len(s.Data["tls.crt"]) == 0 {
							return fmt.Errorf("secret %s/%s has empty tls.crt", ns, secret)
						}
						if len(s.Data["tls.key"]) == 0 {
							return fmt.Errorf("secret %s/%s has empty tls.key", ns, secret)
						}
						return nil
					}, bootstrapTimeout, bootstrapInterval).Should(gomega.Succeed(),
						"controller must bootstrap the webhook-cert secret via the cert Role")
				},
			)

			ginkgo.It(
				"should inject the CA bundle into the validating webhook and conversion CRDs", func() {
					for _, name := range rbgwebhook.ValidatingWebhookConfigurations() {
						gomega.Eventually(func() error {
							vwc, err := f.Clientset.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(f.Ctx, name, metav1.GetOptions{})
							if err != nil {
								return err
							}
							if len(vwc.Webhooks) == 0 {
								return fmt.Errorf("ValidatingWebhookConfiguration %s has no webhooks", name)
							}
							for _, wh := range vwc.Webhooks {
								if len(wh.ClientConfig.CABundle) == 0 {
									return fmt.Errorf("webhook %q in %s has empty caBundle", wh.Name, name)
								}
							}
							return nil
						}, bootstrapTimeout, bootstrapInterval).Should(gomega.Succeed(),
							"caBundle must be injected into ValidatingWebhookConfiguration %s", name)
					}

					crdClient := newCRDClient()
					for _, name := range rbgwebhook.ConversionWebhookCRDs() {
						gomega.Eventually(func() error {
							crd := &apiextv1.CustomResourceDefinition{}
							if err := crdClient.Get(f.Ctx, client.ObjectKey{Name: name}, crd); err != nil {
								return err
							}
							if crd.Spec.Conversion == nil || crd.Spec.Conversion.Webhook == nil ||
								crd.Spec.Conversion.Webhook.ClientConfig == nil {
								return fmt.Errorf("CRD %s has no conversion webhook clientConfig", name)
							}
							if len(crd.Spec.Conversion.Webhook.ClientConfig.CABundle) == 0 {
								return fmt.Errorf("CRD %s conversion webhook has empty caBundle", name)
							}
							return nil
						}, bootstrapTimeout, bootstrapInterval).Should(gomega.Succeed(),
							"caBundle must be injected into conversion CRD %s", name)
					}
				},
			)
		},
	)
}

// subjectsHaveServiceAccount reports whether subjects include the given ServiceAccount.
func subjectsHaveServiceAccount(subjects []rbacv1.Subject, name, namespace string) bool {
	for _, s := range subjects {
		if s.Kind == rbacv1.ServiceAccountKind && s.Name == name && s.Namespace == namespace {
			return true
		}
	}
	return false
}

// secretVerbAllowed reports whether the Role grants verb on core "secrets". When name is
// non-empty, the matching rule must either be unrestricted or list that resourceName.
func secretVerbAllowed(role *rbacv1.Role, verb, name string) bool {
	for _, r := range role.Rules {
		if !slices.Contains(r.APIGroups, "") && !slices.Contains(r.APIGroups, "*") {
			continue
		}
		if !slices.Contains(r.Resources, "secrets") && !slices.Contains(r.Resources, "*") {
			continue
		}
		if !slices.Contains(r.Verbs, verb) && !slices.Contains(r.Verbs, "*") {
			continue
		}
		if name == "" || len(r.ResourceNames) == 0 || slices.Contains(r.ResourceNames, name) {
			return true
		}
	}
	return false
}

// newCRDClient builds a read client that understands CustomResourceDefinition, which the
// shared framework scheme does not register.
func newCRDClient() client.Client {
	scheme := runtime.NewScheme()
	utilruntime.Must(apiextv1.AddToScheme(scheme))
	c, err := client.New(config.GetConfigOrDie(), client.Options{Scheme: scheme})
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "build CRD client")
	return c
}

// dumpBootstrapDebugInfo prints the state of the RBAC + webhook-cert objects when the
// current spec failed, to make RBAC/cert regressions diagnosable from CI logs.
func dumpBootstrapDebugInfo(f *framework.Framework, ns string) {
	if !ginkgo.CurrentSpecReport().Failed() {
		return
	}
	fmt.Printf("\n========== webhook-cert bootstrap debug (namespace %s) ==========\n", ns)

	if role, err := f.Clientset.RbacV1().Roles(ns).Get(f.Ctx, certRoleName, metav1.GetOptions{}); err != nil {
		fmt.Printf("Role %s: %v\n", certRoleName, err)
	} else {
		fmt.Printf("Role %s rules: %+v\n", certRoleName, role.Rules)
	}

	if s, err := f.Clientset.CoreV1().Secrets(ns).Get(f.Ctx, rbgwebhook.WebhookCertSecretName, metav1.GetOptions{}); err != nil {
		fmt.Printf("Secret %s: %v\n", rbgwebhook.WebhookCertSecretName, err)
	} else {
		keys := make([]string, 0, len(s.Data))
		for k := range s.Data {
			keys = append(keys, k)
		}
		fmt.Printf("Secret %s data keys: %v\n", rbgwebhook.WebhookCertSecretName, keys)
	}

	pods, err := f.Clientset.CoreV1().Pods(ns).List(f.Ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("list pods: %v\n", err)
		return
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		fmt.Printf("pod %s phase=%s\n", p.Name, p.Status.Phase)
	}
}
