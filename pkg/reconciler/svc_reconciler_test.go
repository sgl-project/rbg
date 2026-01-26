// svc_reconciler_test.go
package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
	"sigs.k8s.io/rbgs/test/wrappers"
)

func TestServiceReconciler_reconcileHeadlessService(t *testing.T) {
	// Setup test environment
	s := scheme.Scheme
	require.NoError(t, workloadsv1alpha1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))

	// Create test objects
	rbg := wrappers.BuildBasicRoleBasedGroup("test-rbg", "default").WithRoles(
		[]workloadsv1alpha1.RoleSpec{
			wrappers.BuildBasicRole("test-role-statefulset").WithWorkload(workloadsv1alpha1.StatefulSetWorkloadType).Obj(),
			wrappers.BuildBasicRole("test-role-instanceset").WithWorkload(workloadsv1alpha1.InstanceSetWorkloadType).Obj(),
		},
	).Obj()

	statefulset := &appsv1.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "StatefulSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[0]),
			Namespace: rbg.Namespace,
			UID:       "test-sts",
		},
	}

	instanceSet := &workloadsv1alpha1.InstanceSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "InstanceSet",
			APIVersion: "workloads.x-k8s.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbg.GetWorkloadName(&rbg.Spec.Roles[1]),
			Namespace: rbg.Namespace,
			UID:       "test-instanceset",
		},
	}

	// Add common labels method mock if needed
	rbg.ObjectMeta.Labels = map[string]string{"app": "test"}

	// Create fake client
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(rbg, statefulset, instanceSet).Build()

	reconciler := NewServiceReconciler(cl)

	t.Run("successful reconciliation", func(t *testing.T) {
		for i := 0; i < len(rbg.Spec.Roles); i++ {
			roleData := &RoleData{
				Spec:         &rbg.Spec.Roles[i],
				WorkloadName: rbg.GetWorkloadName(&rbg.Spec.Roles[i]),
				OwnerInfo: OwnerInfo{
					Name:      rbg.Name,
					Namespace: rbg.Namespace,
					UID:       rbg.UID,
				},
			}

			err := reconciler.reconcileHeadlessService(context.TODO(), roleData)
			assert.NoError(t, err)

			// Check that service was created
			svcName, _ := utils.GetCompatibleHeadlessServiceName(context.TODO(), cl, rbg, &rbg.Spec.Roles[i])
			svc := &corev1.Service{}
			err = cl.Get(context.TODO(), types.NamespacedName{Name: svcName, Namespace: rbg.Namespace}, svc)
			assert.NoError(t, err)
			assert.Equal(t, "None", svc.Spec.ClusterIP)
			assert.True(t, svc.Spec.PublishNotReadyAddresses)

			// Verify owner reference
			assert.Len(t, svc.OwnerReferences, 1)
			assert.Equal(t, rbg.GetWorkloadName(&rbg.Spec.Roles[i]), svc.OwnerReferences[0].Name)
		}

	})
}

func TestSemanticallyEqualService(t *testing.T) {
	baseSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-svc",
			Namespace: "default",
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": "test",
			},
		},
	}

	t.Run("services are equal", func(t *testing.T) {
		svc1 := baseSvc.DeepCopy()
		svc2 := baseSvc.DeepCopy()

		equal, err := semanticallyEqualService(svc1, svc2)
		assert.True(t, equal)
		assert.NoError(t, err)
	})

	t.Run("services differ in selector", func(t *testing.T) {
		svc1 := baseSvc.DeepCopy()
		svc2 := baseSvc.DeepCopy()
		svc2.Spec.Selector["version"] = "v2"

		equal, err := semanticallyEqualService(svc1, svc2)
		assert.False(t, equal)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "selector not equal")
	})

	t.Run("one service is nil", func(t *testing.T) {
		equal, err := semanticallyEqualService(nil, baseSvc)
		assert.False(t, equal)
		assert.Error(t, err)
	})
}
