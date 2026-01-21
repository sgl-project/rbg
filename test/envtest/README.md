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

# Run all tests
go test -v ./test/envtest/testcase/...
```

### Run Specific Controller Tests

```bash
# Run RBG controller tests
go test -v ./test/envtest/testcase/rbg/

# Run InstanceSet controller tests
go test -v ./test/envtest/testcase/instanceset/

# Run RoleBasedGroupSet controller tests
go test -v ./test/envtest/testcase/rbgs/
```

### Run Specific Tests

```bash
# Use -ginkgo.focus to run specific tests
go test -v ./test/envtest/testcase/rbg/ -ginkgo.focus="basic creation"
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
├── README.md                    # This file
├── doc.go                       # Package documentation
├── testutil/                    # Shared test utilities
│   └── setup.go                 # Test environment setup functions
└── testcase/                    # Test cases organized by controller
    ├── rbg/                     # RoleBasedGroup controller tests
    │   ├── suite_test.go
    │   ├── basic_test.go
    │   ├── advanced_test.go
    │   ├── coordination_test.go
    │   ├── dependency_test.go
    │   └── update_test.go
    ├── rbgs/                    # RoleBasedGroupSet controller tests
    │   ├── suite_test.go
    │   ├── controller_test.go
    │   └── extended_test.go
    ├── instanceset/             # InstanceSet controller tests
    │   ├── suite_test.go
    │   ├── controller_test.go
    │   └── advanced_test.go
    ├── instance/                # Instance controller tests
    │   ├── suite_test.go
    │   └── controller_test.go
    ├── pod/                     # Pod controller tests
    │   ├── suite_test.go
    │   └── controller_test.go
    └── scalingadapter/          # ScalingAdapter controller tests
        ├── suite_test.go
        ├── controller_test.go
        └── extended_test.go
```

## Test Conventions

### Namespace Naming

Each test case should use an independent namespace:

```go
import "sigs.k8s.io/rbgs/test/envtest/testutil"

testNs := "test-rbg-basic-create"
testutil.CreateNamespace(testNs)
defer testutil.DeleteNamespace(testNs)
```

### Timeout and Retry

Use `Eventually` and `Consistently` for asynchronous operations:

```go
// Wait for resource creation
Eventually(func() error {
    return testutil.K8sClient.Get(testutil.Ctx, key, obj)
}, timeout, interval).Should(Succeed())

// Verify state stability
Consistently(func() int32 {
    _ = testutil.K8sClient.Get(testutil.Ctx, key, obj)
    return obj.Status.Replicas
}, duration, interval).Should(Equal(int32(3)))
```

### Resource Cleanup

Always clean up resources after tests:

```go
AfterEach(func() {
    // Delete test object
    _ = testutil.K8sClient.Delete(testutil.Ctx, testObj)
    
    // Delete namespace
    testutil.DeleteNamespace(testNs)
})
```

## Writing New Tests

Refer to test files in `testcase/` directory for examples.

### Basic Template

```go
import "sigs.k8s.io/rbgs/test/envtest/testutil"

var _ = Describe("Feature Name", func() {
    var (
        testNs string
    )

    BeforeEach(func() {
        testNs = fmt.Sprintf("test-feature-%d", time.Now().UnixNano())
        testutil.CreateNamespace(testNs)
    })

    AfterEach(func() {
        testutil.DeleteNamespace(testNs)
    })

    Context("When testing scenario", func() {
        It("Should behave correctly", func() {
            // Use testutil.K8sClient and testutil.Ctx
            Expect(testutil.K8sClient.Create(testutil.Ctx, obj)).Should(Succeed())
        })
    })
})
```

### Suite Setup

Each controller test directory has its own `suite_test.go`:

```go
package mycontroller

import (
    "testing"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "sigs.k8s.io/rbgs/test/envtest/testutil"
)

func TestMyController(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "My Controller Suite")
}

var _ = BeforeSuite(func() {
    testutil.SetupTestEnv()
})

var _ = AfterSuite(func() {
    testutil.TeardownTestEnv()
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
    return testutil.K8sClient.Get(testutil.Ctx, key, obj)
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
