package framework

import (
	"context"
	"flag"

	"github.com/onsi/gomega"
	rawzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	workloadsv1alpha1 "sigs.k8s.io/rbgs/api/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/test/utils"
)

type Framework struct {
	Ctx       context.Context
	Client    client.Client
	Namespace string
}

func NewFramework(development bool) *Framework {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(workloadsv1alpha1.AddToScheme(scheme))
	utilruntime.Must(lwsv1.AddToScheme(scheme))

	cfg := config.GetConfigOrDie()
	runtimeClient, err := client.New(cfg, client.Options{Scheme: scheme})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	ctx := initLogger(context.TODO(), development)

	return &Framework{
		Ctx:    ctx,
		Client: runtimeClient,
	}
}

func initLogger(ctx context.Context, development bool) context.Context {
	opts := zap.Options{
		Development: development,
		EncoderConfigOptions: []zap.EncoderConfigOption{
			func(ec *zapcore.EncoderConfig) {
				ec.MessageKey = "message"
				ec.LevelKey = "level"
				ec.TimeKey = "time"
				ec.CallerKey = "caller"
				ec.EncodeLevel = zapcore.CapitalLevelEncoder
				ec.EncodeCaller = zapcore.ShortCallerEncoder
				ec.EncodeTime = zapcore.ISO8601TimeEncoder
			},
		},
		ZapOpts: []rawzap.Option{
			rawzap.AddCaller(),
		},
	}
	opts.BindFlags(flag.CommandLine)

	logger := zap.New(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(logger)
	return log.IntoContext(ctx, logger)
}

func (f *Framework) BeforeAll() {
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-ns-",
			Labels: map[string]string{
				"rbgs-e2e-test": "true",
			},
		},
	}
	err := f.Client.Create(f.Ctx, ns)
	if err != nil && !errors.IsAlreadyExists(err) {
		gomega.Expect(err).Should(gomega.Succeed(), "Failed to create namespace: %v", err)
	}

	f.Namespace = ns.Name

	gomega.Eventually(
		func() bool {
			err := f.Client.Get(f.Ctx, types.NamespacedName{Name: ns.Name}, ns)
			return err == nil
		}, utils.Timeout, utils.Interval,
	).Should(gomega.BeTrue())
}

func (f *Framework) AfterAll() {
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: f.Namespace,
		},
	}
	gomega.Expect(f.Client.Delete(f.Ctx, ns)).Should(gomega.Succeed())
}

func (f *Framework) AfterEach() {
	gomega.Expect(
		f.Client.DeleteAllOf(
			f.Ctx, &workloadsv1alpha1.RoleBasedGroup{},
			client.InNamespace(f.Namespace),
		),
	).Should(gomega.Succeed())

	gomega.Expect(
		f.Client.DeleteAllOf(
			f.Ctx, &workloadsv1alpha1.RoleBasedGroupSet{},
			client.InNamespace(f.Namespace),
		),
	).Should(gomega.Succeed())
}
