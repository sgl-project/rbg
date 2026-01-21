# Envtest Test Suite

This directory contains integration tests using the controller-runtime envtest framework.

## Prerequisites

### Install setup-envtest

Envtest requires Kubernetes test binaries (etcd, kube-apiserver, etc.). Install them using the setup-envtest tool:

```bash
# Install setup-envtest
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

# Install test binaries
setup-envtest use -p path

# Set environment variable (add to shell profile or run before tests)
export KUBEBUILDER_ASSETS="$(setup-envtest use -p path)"
```

Or use the Makefile shortcut (if provided by the project):

```bash
make envtest
```

### Verify Installation

```bash
# Check envtest binary path
setup-envtest use -p path

# Verify binaries exist
ls $(setup-envtest use -p path)
# Should see: etcd  kube-apiserver  kubectl
```

## Running Tests

### Run All Tests

```bash
# Set environment variable
export KUBEBUILDER_ASSETS="$(setup-envtest use -p path)"

# Run tests
go test -v ./test/envtest/...
```

### Run Specific Tests

```bash
# Use -ginkgo.focus to run specific tests
go test -v ./test/envtest -ginkgo.focus="Hello World"

# Run tests from a specific file
go test -v ./test/envtest -run TestControllers
```

### Debug Mode

```bash
# Enable verbose logging
go test -v ./test/envtest -ginkgo.v

# Skip certain tests
go test -v ./test/envtest -ginkgo.skip="slow tests"
```

## Test Structure

```
test/envtest/
├── README.md                # This file
├── TEST_PLAN.md            # Detailed test plan and cases
├── suite_test.go           # Test suite setup and common utilities
├── helloworld_test.go      # Hello World example tests
├── rbg_controller_test.go  # RoleBasedGroup controller tests
├── pod_controller_test.go  # Pod controller tests
├── rbgscalingadapter_controller_test.go  # ScalingAdapter controller tests
├── rbgs_controller_test.go              # RoleBasedGroupSet controller tests
├── instance_controller_test.go          # Instance controller tests
└── instanceset_controller_test.go       # InstanceSet controller tests
```

## Test Conventions

### Namespace Naming

Each test case should use an independent namespace:

```go
testNs := "test-rbg-basic-create"
createNamespace(testNs)
defer deleteNamespace(testNs)
```

### Timeout and Retry

Use `Eventually` and `Consistently` for asynchronous operations:

```go
// Wait for resource creation
Eventually(func() error {
    return k8sClient.Get(ctx, key, obj)
}, timeout, interval).Should(Succeed())

// Verify state stability
Consistently(func() int32 {
    _ = k8sClient.Get(ctx, key, obj)
    return obj.Status.Replicas
}, duration, interval).Should(Equal(int32(3)))
```

### Resource Cleanup

Always clean up resources after tests:

```go
AfterEach(func() {
    // Delete test object
    _ = k8sClient.Delete(ctx, testObj)
    
    // Delete namespace
    deleteNamespace(testNs)
})
```

## Writing New Tests

Refer to [TEST_PLAN.md](./TEST_PLAN.md) for test cases that need to be covered.

### Basic Template

```go
var _ = Describe("Feature Name", func() {
    var (
        testNs string
    )

    BeforeEach(func() {
        testNs = "test-feature-" + generateRandomString(5)
        createNamespace(testNs)
    })

    AfterEach(func() {
        deleteNamespace(testNs)
    })

    Context("When testing scenario", func() {
        It("Should behave correctly", func() {
            // Test logic
        })
    })
})
```

### Controller Manager Testing

If you need to test controller reconcile logic:

```go
var _ = Describe("RBG Controller", func() {
    var (
        mgr    manager.Manager
        ctx    context.Context
        cancel context.CancelFunc
    )

    BeforeEach(func() {
        ctx, cancel = context.WithCancel(context.Background())
        
        var err error
        mgr, err = setupManager(ctx)
        Expect(err).NotTo(HaveOccurred())
        
        err = setupRBGController(mgr)
        Expect(err).NotTo(HaveOccurred())
        
        // Start manager
        go func() {
            defer GinkgoRecover()
            err := mgr.Start(ctx)
            Expect(err).NotTo(HaveOccurred())
        }()
    })

    AfterEach(func() {
        cancel()
    })

    // Test cases...
})
```

## Common Issues

### Q: Test fails with "unable to start control plane"

**A:** Need to install envtest binaries:

```bash
export KUBEBUILDER_ASSETS="$(setup-envtest use -p path)"
```

### Q: Test timeout

**A:** Increase timeout or check test logic:

```go
Eventually(func() error {
    return k8sClient.Get(ctx, key, obj)
}, time.Second*30, time.Millisecond*500).Should(Succeed())
```

### Q: CRD not found

**A:** Ensure CRD file path is correct:

```go
testEnv = &envtest.Environment{
    CRDDirectoryPaths: []string{
        filepath.Join("..", "..", "config", "crd", "bases"),
    },
}
```

### Q: How to debug failed tests

**A:** Use GinkgoWriter for debug output:

```go
fmt.Fprintf(GinkgoWriter, "Debug info: %+v\n", obj)
```

## CI/CD Integration

Running in CI environment:

```yaml
# GitHub Actions example
- name: Setup envtest
  run: |
    go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
    echo "KUBEBUILDER_ASSETS=$(setup-envtest use -p path)" >> $GITHUB_ENV

- name: Run envtest
  run: |
    make test-envtest
```

## References

- [controller-runtime envtest documentation](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
- [Ginkgo testing framework](https://onsi.github.io/ginkgo/)
- [Gomega assertion library](https://onsi.github.io/gomega/)
- [TEST_PLAN.md](./TEST_PLAN.md) - Detailed test plan
