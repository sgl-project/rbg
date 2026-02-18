package benchmark

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"sigs.k8s.io/rbgs/cmd/cli/util"
)

const (
	defaultDashboardImage   = "rolebasedgroup/rbgs-benchmark-dashboard:v0.6.0"
	defaultDashboardPort    = 8080
	defaultLocalPort        = 18888
	dashboardLabelKey       = "rbg-benchmark-dashboard-app"
	dashboardLabelValue     = "rbg-benchmark-dashboard"
	dashboardContainerName  = "benchmark-dashboard"
	dashboardDataMountPath  = "/data"
	dashboardDataVolumeName = "data-volume"
)

// DashboardOptions holds configuration for the dashboard command.
type DashboardOptions struct {
	cf *genericclioptions.ConfigFlags

	experimentBaseDir string
	localPort         int
	openBrowser       bool
	image             string
}

var dashboardOpts DashboardOptions

// NewBenchmarkDashboardCmd creates the "llm benchmark dashboard" command.
func NewBenchmarkDashboardCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	dashboardOpts = DashboardOptions{
		cf:          cf,
		localPort:   defaultLocalPort,
		openBrowser: true,
		image:       defaultDashboardImage,
	}

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Start a web dashboard to browse benchmark results",
		Long: `Start an HTTP server that displays benchmark results stored in a PVC.

The command creates a temporary Pod in the cluster that mounts the PVC,
starts an HTTP server, and automatically sets up port-forwarding to your
local machine. A browser window will open to view the results.

Press Ctrl+C to stop the dashboard and clean up resources.`,
		Example: `  # Start the dashboard to browse benchmark results
  kubectl rbg llm benchmark dashboard --experiment-base-dir pvc://benchmark-output/rbg-benchmark

  # Use a custom local port
  kubectl rbg llm benchmark dashboard --experiment-base-dir pvc://benchmark-output/rbg-benchmark --port 9000

  # Don't automatically open the browser
  kubectl rbg llm benchmark dashboard --experiment-base-dir pvc://benchmark-output/rbg-benchmark --open-browser=false`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDashboard(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&dashboardOpts.experimentBaseDir, "experiment-base-dir", "",
		"PVC path containing experiment results (e.g. pvc://benchmark-output/rbg-benchmark)")
	_ = cmd.MarkFlagRequired("experiment-base-dir")

	cmd.Flags().IntVar(&dashboardOpts.localPort, "port", defaultLocalPort,
		"Local port for port-forward")
	cmd.Flags().BoolVar(&dashboardOpts.openBrowser, "open-browser", true,
		"Automatically open browser after dashboard is ready")
	cmd.Flags().StringVar(&dashboardOpts.image, "image", defaultDashboardImage,
		"Container image for the benchmark dashboard")

	return cmd
}

// runDashboard creates a Pod, sets up port-forwarding, and opens a browser.
func runDashboard(ctx context.Context) error {
	if dashboardOpts.cf == nil {
		return fmt.Errorf("kubeconfig flags are not initialized")
	}

	// Validate PVC path
	if !isPVCStorageURI(dashboardOpts.experimentBaseDir) {
		return fmt.Errorf("--experiment-base-dir must be a PVC path (e.g. pvc://benchmark-output/rbg-benchmark)")
	}

	pvcComponents, err := parsePVCStorageURI(dashboardOpts.experimentBaseDir)
	if err != nil {
		return fmt.Errorf("failed to parse PVC URI: %w", err)
	}

	ns := util.GetNamespace(dashboardOpts.cf)
	clientset, err := util.GetK8SClientSet(dashboardOpts.cf)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes clientset: %w", err)
	}

	// Build and create the dashboard Pod
	pod := buildDashboardPod(ns, pvcComponents)
	createdPod, err := clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create dashboard pod: %w", err)
	}
	podName := createdPod.Name
	fmt.Printf("Created benchmark dashboard Pod: %s\n", podName)

	// Set up cleanup function using sync.Once to ensure it runs exactly once
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "Cleaning up Pod %s...\n", podName)
			deleteCtx, cancelDelete := context.WithTimeout(context.TODO(), 30*time.Second)
			defer cancelDelete()

			err := clientset.CoreV1().Pods(ns).Delete(deleteCtx, podName, metav1.DeleteOptions{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to delete pod: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "Pod %s deleted successfully\n", podName)
			}
		})
	}

	// Create a cancellable context
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Use defer to ensure cleanup runs on any exit path
	defer cleanup()

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start a goroutine to handle signals during waiting phases
	// and directly trigger cleanup
	go func() {
		sig := <-sigChan
		// Use Stderr for immediate output (no buffering)
		fmt.Fprintf(os.Stderr, "\nReceived signal: %v, stopping...\n", sig)
		// Perform cleanup synchronously
		cleanup()
		// Cancel context to interrupt any waiting operations
		cancel()
		fmt.Fprintln(os.Stderr, "Cleanup complete, exiting...")
	}()

	// Wait for Pod to be ready
	fmt.Printf("Waiting for Pod to be ready...\n")
	if err := waitForPodReady(ctx, clientset, ns, podName, true); err != nil {
		if ctx.Err() != nil {
			return nil // Interrupted by signal, cleanup will run via defer
		}
		return fmt.Errorf("pod failed to become ready: %w", err)
	}
	fmt.Printf("Pod is ready\n")

	// Check if already cancelled
	if ctx.Err() != nil {
		return nil
	}

	// Get kubeconfig path for port-forwarding
	kubeconfig := ""
	if dashboardOpts.cf.KubeConfig != nil {
		kubeconfig = *dashboardOpts.cf.KubeConfig
	}

	// Start port-forward
	stopChan := make(chan struct{})
	readyChan := make(chan struct{})
	resultChan := make(chan PortForwardResult, 1)

	go startPortForward(kubeconfig, ns, podName, dashboardOpts.localPort, defaultDashboardPort, stopChan, readyChan, resultChan)

	// Wait for port-forward to be ready or fail
	select {
	case <-readyChan:
		fmt.Printf("Port-forward established: localhost:%d -> %s:%d\n", dashboardOpts.localPort, podName, defaultDashboardPort)
	case result := <-resultChan:
		if result.Err != nil {
			return result.Err
		}
	case <-ctx.Done():
		close(stopChan)
		return nil
	case <-time.After(30 * time.Second):
		close(stopChan)
		return fmt.Errorf("timeout waiting for port-forward")
	}

	url := fmt.Sprintf("http://localhost:%d", dashboardOpts.localPort)
	fmt.Printf("\nBenchmark results dashboard is running at: %s\n", url)
	fmt.Printf("Press Ctrl+C to stop\n\n")

	// Open browser if requested
	if dashboardOpts.openBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Printf("Warning: failed to open browser: %v\n", err)
			fmt.Printf("Please open %s manually\n", url)
		}
	}

	// Wait for interrupt or port-forward error
	select {
	case <-ctx.Done():
		// Context was cancelled by earlier signal
	case result := <-resultChan:
		if result.Err != nil {
			fmt.Printf("\nPort-forward terminated: %v\n", result.Err)
		}
	}

	close(stopChan)
	// cleanup() will be called by defer
	return nil
}

// openBrowser opens the specified URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
