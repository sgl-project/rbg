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
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
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
		"controller RBAC and webhook-cert bootstrap", ginkgo.Label("rbac", "install"), func() {
			ns := controllerNamespace()

			ginkgo.BeforeEach(func() {
				f.RegisterDebugFn(func() { dumpBootstrapDebugInfo(f, ns) })
			})

			ginkgo.It(
				"should install the controller RBAC objects with resolvable bindings", ginkgo.Label("smoke"), func() {
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
				"should scope the webhook-cert Role to exactly secret create + rbgs-webhook-cert get/update", func() {
					role, err := f.Clientset.RbacV1().Roles(ns).Get(f.Ctx, certRoleName, metav1.GetOptions{})
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "Role %s/%s should exist", ns, certRoleName)

					// Assert the exact least-privilege grant set, so this case also fails if the
					// Role is broadened (extra verbs, unrestricted get/update, wildcards, or any
					// resource other than core secrets) — not just if a needed verb is missing.
					secret := rbgwebhook.WebhookCertSecretName
					gomega.Expect(secretRuleViolations(role, secret)).Should(gomega.BeEmpty(),
						"cert Role must grant exactly: create secrets; get/update on %s only", secret)
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
				"should inject the CA bundle into the admission webhooks and conversion CRDs", func() {
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

					for _, name := range rbgwebhook.MutatingWebhookConfigurations() {
						gomega.Eventually(func() error {
							mwc, err := f.Clientset.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(f.Ctx, name, metav1.GetOptions{})
							if err != nil {
								return err
							}
							if len(mwc.Webhooks) == 0 {
								return fmt.Errorf("MutatingWebhookConfiguration %s has no webhooks", name)
							}
							for _, wh := range mwc.Webhooks {
								if len(wh.ClientConfig.CABundle) == 0 {
									return fmt.Errorf("webhook %q in %s has empty caBundle", wh.Name, name)
								}
							}
							return nil
						}, bootstrapTimeout, bootstrapInterval).Should(gomega.Succeed(),
							"caBundle must be injected into MutatingWebhookConfiguration %s", name)
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

			ginkgo.It(
				"should authorize metrics with a ServiceAccount token", ginkgo.Label("metrics"), func() {
					ctx, cancel := context.WithTimeout(f.Ctx, bootstrapTimeout)
					defer cancel()

					allowedToken, cleanupAllowed, err := createMetricsServiceAccount(ctx, f, ns, true)
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "create metrics reader ServiceAccount")
					ginkgo.DeferCleanup(cleanupAllowed)

					deniedToken, cleanupDenied, err := createMetricsServiceAccount(ctx, f, ns, false)
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "create metrics denied ServiceAccount")
					ginkgo.DeferCleanup(cleanupDenied)

					metricsEndpoint, stopPortForward, err := startMetricsPortForward(ctx, ns)
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "port-forward controller metrics")
					ginkgo.DeferCleanup(stopPortForward)

					status, body, err := requestMetrics(ctx, metricsEndpoint, "")
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "request metrics without credentials")
					gomega.Expect(status).Should(gomega.Equal(http.StatusUnauthorized), "body: %s", body)

					status, body, err = requestMetrics(ctx, metricsEndpoint, deniedToken)
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "request metrics with unauthorized ServiceAccount")
					gomega.Expect(status).Should(gomega.Equal(http.StatusForbidden), "body: %s", body)

					status, body, err = requestMetrics(ctx, metricsEndpoint, allowedToken)
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred(), "request metrics with authorized ServiceAccount")
					gomega.Expect(status).Should(gomega.Equal(http.StatusOK), "body: %s", body)
					gomega.Expect(body).Should(gomega.ContainSubstring("# HELP"))
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

// secretRuleViolations returns human-readable reasons the cert Role deviates from the
// intended least-privilege grant set — create on secrets (any name), plus get/update on
// only the named webhook-cert secret. An empty result means the Role is exactly scoped.
// It reports both missing grants and over-permissive ones (extra verbs, unrestricted
// get/update, wildcards, or any resource beyond core secrets).
func secretRuleViolations(role *rbacv1.Role, secretName string) []string {
	// Expected effective grants keyed as "verb|comma-joined-sorted-resourceNames".
	expected := map[string]bool{
		"create|":              true, // create cannot be resourceName-scoped
		"get|" + secretName:    true,
		"update|" + secretName: true,
	}

	var violations []string
	got := map[string]bool{}
	for _, r := range role.Rules {
		if len(r.APIGroups) != 1 || r.APIGroups[0] != "" || len(r.Resources) != 1 || r.Resources[0] != "secrets" {
			violations = append(violations, fmt.Sprintf("rule grants beyond core secrets: groups=%v resources=%v", r.APIGroups, r.Resources))
			continue
		}
		names := append([]string(nil), r.ResourceNames...)
		slices.Sort(names)
		scope := strings.Join(names, ",")
		for _, v := range r.Verbs {
			got[v+"|"+scope] = true
		}
	}
	for g := range got {
		if !expected[g] {
			violations = append(violations, "unexpected grant: "+g)
		}
	}
	for e := range expected {
		if !got[e] {
			violations = append(violations, "missing grant: "+e)
		}
	}
	return violations
}

func createMetricsServiceAccount(
	ctx context.Context, f *framework.Framework, namespace string, allowMetrics bool,
) (string, func(), error) {
	sa, err := f.Clientset.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "metrics-reader-"},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", func() {}, err
	}

	roleName := ""
	bindingName := ""
	cleanup := func() {
		deleteCtx, cancel := context.WithTimeout(context.Background(), bootstrapTimeout)
		defer cancel()

		if bindingName != "" {
			_ = f.Clientset.RbacV1().ClusterRoleBindings().Delete(deleteCtx, bindingName, metav1.DeleteOptions{})
		}
		if roleName != "" {
			_ = f.Clientset.RbacV1().ClusterRoles().Delete(deleteCtx, roleName, metav1.DeleteOptions{})
		}
		_ = f.Clientset.CoreV1().ServiceAccounts(namespace).Delete(deleteCtx, sa.Name, metav1.DeleteOptions{})
	}

	if allowMetrics {
		role, err := f.Clientset.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "metrics-reader-"},
			Rules: []rbacv1.PolicyRule{{
				Verbs:           []string{"get"},
				NonResourceURLs: []string{"/metrics"},
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		roleName = role.Name

		binding, err := f.Clientset.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "metrics-reader-"},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     role.Name,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      sa.Name,
				Namespace: namespace,
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		bindingName = binding.Name
	}

	tokenRequest, err := f.Clientset.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, sa.Name, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To(int64(600))},
	}, metav1.CreateOptions{})
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if tokenRequest.Status.Token == "" {
		cleanup()
		return "", func() {}, fmt.Errorf("TokenRequest for ServiceAccount %s/%s returned an empty token", namespace, sa.Name)
	}

	return tokenRequest.Status.Token, cleanup, nil
}

func startMetricsPortForward(ctx context.Context, namespace string) (string, func(), error) {
	clientset, err := kubernetes.NewForConfig(config.GetConfigOrDie())
	if err != nil {
		return "", func() {}, err
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "control-plane=rbgs-controller",
	})
	if err != nil {
		return "", func() {}, err
	}
	var pod *corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			pod = &pods.Items[i]
			break
		}
	}
	if pod == nil {
		return "", func() {}, fmt.Errorf("no running controller pod found in namespace %s", namespace)
	}

	if _, err := exec.LookPath("kubectl"); err != nil {
		return "", func() {}, fmt.Errorf("kubectl is required for metrics port-forward: %w", err)
	}
	command := exec.CommandContext(ctx, "kubectl", "--namespace", namespace, "port-forward",
		"pod/"+pod.Name, "--address", "127.0.0.1", ":8443")
	command.Stderr = io.Discard
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", func() {}, err
	}
	if err := command.Start(); err != nil {
		return "", func() {}, err
	}

	processDone := make(chan error, 1)
	go func() {
		processDone <- command.Wait()
	}()

	forwardedPort := make(chan int, 1)
	scanDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			prefix := "Forwarding from 127.0.0.1:"
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			portText, _, ok := strings.Cut(strings.TrimPrefix(line, prefix), " -> ")
			if !ok {
				continue
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				continue
			}
			forwardedPort <- port
		}
		scanDone <- scanner.Err()
	}()

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if command.Process != nil {
				_ = command.Process.Kill()
			}
			select {
			case <-processDone:
			case <-time.After(bootstrapInterval):
			}
		})
	}

	select {
	case port := <-forwardedPort:
		return fmt.Sprintf("https://127.0.0.1:%d/metrics", port), stop, nil
	case err := <-processDone:
		return "", func() {}, fmt.Errorf("start port-forward: %w", err)
	case err := <-scanDone:
		if err != nil {
			return "", func() {}, fmt.Errorf("read port-forward output: %w", err)
		}
		return "", func() {}, fmt.Errorf("port-forward output closed before becoming ready")
	case <-ctx.Done():
		stop()
		return "", func() {}, ctx.Err()
	}
}

func requestMetrics(ctx context.Context, endpoint, token string) (int, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, "", err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	return response.StatusCode, string(body), err
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
