package generate

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/klog/v2"
	applyconfiguration "sigs.k8s.io/rbgs/client-go/applyconfiguration/workloads/v1alpha1"
	"sigs.k8s.io/rbgs/pkg/utils"
)

// RenderDeploymentYAML generates RBG deployment YAML from generator config
func RenderDeploymentYAML(plan *DeploymentPlan) error {
	var yamlContent string
	var err error

	switch plan.Mode {
	case "disagg":
		yamlContent, err = renderDisaggYAML(plan)
	case "agg":
		yamlContent, err = renderAggYAML(plan)
	default:
		return fmt.Errorf("unknown deployment mode: %s", plan.Mode)
	}

	if err != nil {
		return fmt.Errorf("failed to render %s YAML: %w", plan.Mode, err)
	}

	// Write YAML to file
	if err := os.WriteFile(plan.OutputPath, []byte(yamlContent), 0644); err != nil {
		return fmt.Errorf("failed to write YAML to %s: %w", plan.OutputPath, err)
	}

	klog.V(2).Infof("Successfully generated %s deployment YAML: %s", plan.Mode, plan.OutputPath)
	return nil
}

// renderDisaggYAML generates YAML for Prefill-Decode disaggregated mode
func renderDisaggYAML(plan *DeploymentPlan) (string, error) {
	config := plan.Config
	prefillParams := GetWorkerParams(config.Params.Prefill)
	decodeParams := GetWorkerParams(config.Params.Decode)

	// Get base name for the deployment
	baseName := getDeployName(plan.ModelName, plan.BackendName, "pd")
	modelPath := getModelPath(plan.ModelName, plan.HuggingFaceID)
	image := getImage(plan.BackendName)

	// Build RoleBasedGroup using builder pattern
	gkv := utils.GetRbgGVK()
	rbg := applyconfiguration.RoleBasedGroup(baseName, "default").
		WithKind(gkv.Kind).
		WithAPIVersion(gkv.GroupVersion().String()).
		WithSpec(applyconfiguration.RoleBasedGroupSpec().
			WithRoles(
				buildRouterRoleSpec(baseName, image, modelPath, plan.BackendName, plan),
				buildPrefillRoleSpec(image, modelPath, plan.BackendName, config.Workers.PrefillWorkers, prefillParams, plan),
				buildDecodeRoleSpec(image, modelPath, plan.BackendName, config.Workers.DecodeWorkers, decodeParams, plan),
			))

	// Build Service
	service := buildServiceSpec(baseName, "router")

	// Combine RBG and Service
	return marshalMultiDocYAML(rbg, service)
}

// renderAggYAML generates YAML for aggregated mode
func renderAggYAML(plan *DeploymentPlan) (string, error) {
	config := plan.Config
	aggParams := GetWorkerParams(config.Params.Agg)

	baseName := getDeployName(plan.ModelName, plan.BackendName, "agg")
	modelPath := getModelPath(plan.ModelName, plan.HuggingFaceID)
	image := getImage(plan.BackendName)

	// Build RoleBasedGroup using builder pattern
	gkv := utils.GetRbgGVK()
	rbg := applyconfiguration.RoleBasedGroup(baseName, "default").
		WithKind(gkv.Kind).
		WithAPIVersion(gkv.GroupVersion().String()).
		WithSpec(applyconfiguration.RoleBasedGroupSpec().
			WithRoles(
				buildWorkerRoleSpec(image, modelPath, plan.BackendName, config.Workers.AggWorkers, aggParams, plan),
			))

	// Build Service
	service := buildServiceSpec(baseName, "worker")

	return marshalMultiDocYAML(rbg, service)
}

// buildRouterRoleSpec creates the router role spec using builder pattern
func buildRouterRoleSpec(baseName, image, modelPath, backend string, plan *DeploymentPlan) *applyconfiguration.RoleSpecApplyConfiguration {
	if backend != BackendSGLang {
		klog.Fatalf("Router role configuration for backend %s not implemented", backend)
	}

	// Build command with dynamic prefill and decode endpoints
	command := []string{
		"python3",
		"-m",
		"sglang_router.launch_router",
		"--pd-disaggregation",
	}

	// Add all prefill worker endpoints
	prefillReplicas := plan.Config.Workers.PrefillWorkers
	for i := 0; i < prefillReplicas; i++ {
		command = append(command, "--prefill")
		command = append(command, fmt.Sprintf("http://%s-prefill-%d.s-%s-prefill:8000", baseName, i, baseName))
	}

	// Add all decode worker endpoints
	decodeReplicas := plan.Config.Workers.DecodeWorkers
	for i := 0; i < decodeReplicas; i++ {
		command = append(command, "--decode")
		command = append(command, fmt.Sprintf("http://%s-decode-%d.s-%s-decode:8000", baseName, i, baseName))
	}

	// Add common parameters
	command = append(command,
		"--host",
		"0.0.0.0",
		"--port",
		"8000",
	)

	podTemplate := applycorev1.PodTemplateSpec().
		WithSpec(applycorev1.PodSpec().
			WithVolumes(
				applycorev1.Volume().
					WithName("model").
					WithPersistentVolumeClaim(applycorev1.PersistentVolumeClaimVolumeSource().
						WithClaimName(normalizeModelName(plan.ModelName))),
			).
			WithContainers(
				applycorev1.Container().
					WithName("schedule").
					WithImage(image).
					WithImagePullPolicy(corev1.PullAlways).
					WithCommand(command...).
					WithVolumeMounts(
						applycorev1.VolumeMount().
							WithName("model").
							WithMountPath(modelPath),
					),
			))

	return applyconfiguration.RoleSpec().
		WithName("router").
		WithDependencies([]string{"prefill", "decode"}...).
		WithReplicas(1).
		WithTemplate(podTemplate)
}

// buildPrefillRoleSpec creates the prefill role spec using builder pattern
func buildPrefillRoleSpec(image, modelPath, backend string, replicas int, params WorkerParams, plan *DeploymentPlan) *applyconfiguration.RoleSpecApplyConfiguration {
	command := buildPrefillCommand(backend, modelPath, params)
	containerName := fmt.Sprintf("%s-prefill", backend)
	podTemplate := buildWorkerPodTemplate(image, modelPath, containerName, command, params.TensorParallelSize, plan, true)

	return applyconfiguration.RoleSpec().
		WithName("prefill").
		WithReplicas(int32(replicas)).
		WithTemplate(podTemplate)
}

// buildDecodeRoleSpec creates the decode role spec using builder pattern
func buildDecodeRoleSpec(image, modelPath, backend string, replicas int, params WorkerParams, plan *DeploymentPlan) *applyconfiguration.RoleSpecApplyConfiguration {
	command := buildDecodeCommand(backend, modelPath, params)
	containerName := fmt.Sprintf("%s-decode", backend)
	podTemplate := buildWorkerPodTemplate(image, modelPath, containerName, command, params.TensorParallelSize, plan, true)

	return applyconfiguration.RoleSpec().
		WithName("decode").
		WithReplicas(int32(replicas)).
		WithTemplate(podTemplate)
}

// buildWorkerRoleSpec creates the worker role spec for aggregated mode using builder pattern
func buildWorkerRoleSpec(image, modelPath, backend string, replicas int, params WorkerParams, plan *DeploymentPlan) *applyconfiguration.RoleSpecApplyConfiguration {
	command := buildAggCommand(backend, modelPath, params)
	containerName := fmt.Sprintf("%s-worker", backend)
	podTemplate := buildWorkerPodTemplate(image, modelPath, containerName, command, params.TensorParallelSize, plan, false)

	return applyconfiguration.RoleSpec().
		WithName("worker").
		WithReplicas(int32(replicas)).
		WithTemplate(podTemplate)
}

// buildWorkerPodTemplate creates a common pod template for worker roles
// withShmSizeLimit controls whether to set a size limit (30Gi) on the shm volume
func buildWorkerPodTemplate(image, modelPath, containerName string, command []string, tensorParallelSize int, plan *DeploymentPlan, withShmSizeLimit bool) *applycorev1.PodTemplateSpecApplyConfiguration {
	gpuQuantity := resource.MustParse(fmt.Sprintf("%d", tensorParallelSize))

	// Build container
	container := applycorev1.Container().
		WithName(containerName).
		WithImage(image).
		WithImagePullPolicy(corev1.PullAlways).
		WithEnv(buildPodIPEnvVar()).
		WithCommand(command...).
		WithPorts(buildHTTPContainerPort()).
		WithReadinessProbe(buildTCPReadinessProbe()).
		WithResources(buildGPUResourceRequirements(gpuQuantity)).
		WithVolumeMounts(
			buildModelVolumeMount(modelPath),
			buildShmVolumeMount(),
		)

	return applycorev1.PodTemplateSpec().
		WithSpec(applycorev1.PodSpec().
			WithVolumes(
				buildModelVolume(plan.ModelName),
				buildShmVolume(withShmSizeLimit),
			).
			WithContainers(container))
}

// buildModelVolume creates a PVC-backed volume for model storage
func buildModelVolume(modelName string) *applycorev1.VolumeApplyConfiguration {
	return applycorev1.Volume().
		WithName("model").
		WithPersistentVolumeClaim(applycorev1.PersistentVolumeClaimVolumeSource().
			WithClaimName(normalizeModelName(modelName)))
}

// buildShmVolume creates a shared memory volume
// withSizeLimit controls whether to set a 30Gi size limit
func buildShmVolume(withSizeLimit bool) *applycorev1.VolumeApplyConfiguration {
	shmVolume := applycorev1.Volume().
		WithName("shm")

	if withSizeLimit {
		shmSize := resource.MustParse("30Gi")
		shmVolume.WithEmptyDir(applycorev1.EmptyDirVolumeSource().
			WithMedium(corev1.StorageMediumMemory).
			WithSizeLimit(shmSize))
	} else {
		shmVolume.WithEmptyDir(applycorev1.EmptyDirVolumeSource().
			WithMedium(corev1.StorageMediumMemory))
	}

	return shmVolume
}

// buildPodIPEnvVar creates the POD_IP environment variable
func buildPodIPEnvVar() *applycorev1.EnvVarApplyConfiguration {
	return applycorev1.EnvVar().
		WithName("POD_IP").
		WithValueFrom(applycorev1.EnvVarSource().
			WithFieldRef(applycorev1.ObjectFieldSelector().
				WithFieldPath("status.podIP")))
}

// buildHTTPContainerPort creates the HTTP container port (8000)
func buildHTTPContainerPort() *applycorev1.ContainerPortApplyConfiguration {
	return applycorev1.ContainerPort().
		WithContainerPort(8000).
		WithName("http")
}

// buildTCPReadinessProbe creates a TCP readiness probe for port 8000
func buildTCPReadinessProbe() *applycorev1.ProbeApplyConfiguration {
	return applycorev1.Probe().
		WithInitialDelaySeconds(30).
		WithPeriodSeconds(10).
		WithTCPSocket(applycorev1.TCPSocketAction().
			WithPort(intstr.FromInt(8000)))
}

// buildGPUResourceRequirements creates GPU resource requirements
func buildGPUResourceRequirements(gpuQuantity resource.Quantity) *applycorev1.ResourceRequirementsApplyConfiguration {
	return applycorev1.ResourceRequirements().
		WithLimits(corev1.ResourceList{
			"nvidia.com/gpu": gpuQuantity,
			// "rdma/hca":       resource.MustParse("1"),
		}).
		WithRequests(corev1.ResourceList{
			"nvidia.com/gpu": gpuQuantity,
			// "rdma/hca":       resource.MustParse("1"),
		})
}

// buildModelVolumeMount creates a volume mount for the model
func buildModelVolumeMount(modelPath string) *applycorev1.VolumeMountApplyConfiguration {
	return applycorev1.VolumeMount().
		WithName("model").
		WithMountPath(modelPath)
}

// buildShmVolumeMount creates a volume mount for shared memory
func buildShmVolumeMount() *applycorev1.VolumeMountApplyConfiguration {
	return applycorev1.VolumeMount().
		WithName("shm").
		WithMountPath("/dev/shm")
}

// buildServiceSpec creates a Kubernetes Service resource using builder pattern
func buildServiceSpec(baseName, targetRole string) *applycorev1.ServiceApplyConfiguration {
	return applycorev1.Service(baseName, "default").
		WithAPIVersion("v1").
		WithKind("Service").
		WithLabels(map[string]string{
			"app": baseName,
		}).
		WithSpec(applycorev1.ServiceSpec().
			WithPorts(
				applycorev1.ServicePort().
					WithName("http").
					WithPort(8000).
					WithProtocol(corev1.ProtocolTCP).
					WithTargetPort(intstr.FromInt(8000)),
			).
			WithSelector(map[string]string{
				"rolebasedgroup.workloads.x-k8s.io/name": baseName,
				"rolebasedgroup.workloads.x-k8s.io/role": targetRole,
			}).
			WithType(corev1.ServiceTypeClusterIP))
}

// buildPrefillCommand constructs the prefill worker command
func buildPrefillCommand(backend, modelPath string, params WorkerParams) []string {
	return buildSGLangCommand(backend, modelPath, params, "prefill")
}

// buildDecodeCommand constructs the decode worker command
func buildDecodeCommand(backend, modelPath string, params WorkerParams) []string {
	return buildSGLangCommand(backend, modelPath, params, "decode")
}

// buildAggCommand constructs the aggregated mode worker command
func buildAggCommand(backend, modelPath string, params WorkerParams) []string {
	return buildSGLangCommand(backend, modelPath, params, "")
}

// buildSGLangCommand builds a SGLang server command with optional disaggregation mode
// disaggMode can be "prefill", "decode", or "" for aggregated mode
func buildSGLangCommand(backend, modelPath string, params WorkerParams, disaggMode string) []string {
	if backend != BackendSGLang {
		return []string{"echo", fmt.Sprintf("Backend %s not yet supported", backend)}
	}

	// Build base command arguments
	args := []string{
		"-m",
		"sglang.launch_server",
		"--model-path",
		modelPath,
		"--enable-metrics",
	}

	// Add disaggregation mode if specified (for prefill/decode)
	if disaggMode != "" {
		args = append(args, "--disaggregation-mode", disaggMode)
	}

	// Add common server parameters
	args = append(args,
		"--port",
		"8000",
		"--host",
		"$(POD_IP)",
	)

	// Add parallelization parameters
	args = appendParallelizationParams(args, params)

	return append([]string{"python3"}, args...)
}

// appendParallelizationParams adds parallelization parameters to the command args
func appendParallelizationParams(args []string, params WorkerParams) []string {
	// Add tensor-parallel-size
	if params.TensorParallelSize > 0 {
		args = append(args, "--tensor-parallel-size", fmt.Sprintf("%d", params.TensorParallelSize))
	}

	// Add pipeline-parallel-size
	if params.PipelineParallelSize > 0 {
		args = append(args, "--pipeline-parallel-size", fmt.Sprintf("%d", params.PipelineParallelSize))
	}

	// Add data-parallel-size
	if params.DataParallelSize > 0 {
		args = append(args, "--data-parallel-size", fmt.Sprintf("%d", params.DataParallelSize))
	}

	// Add expert-parallel-size
	if params.MoEExpertParallelSize > 0 {
		args = append(args, "--expert-parallel-size", fmt.Sprintf("%d", params.MoEExpertParallelSize))
	}

	// Add moe-dense-tp-size
	if params.MoETensorParallelSize > 0 {
		args = append(args, "--moe-dense-tp-size", fmt.Sprintf("%d", params.MoETensorParallelSize))
	}

	return args
}

// getDeployName generates a deploy name with a random suffix to avoid conflicts
// The suffix is a 5-character lowercase hex string that complies with DNS naming rules
func getDeployName(modelName, backend, suffix string) string {
	// Convert model name to lowercase and replace underscores
	name := strings.ToLower(strings.ReplaceAll(modelName, "_", "-"))
	// Generate a random 5-character suffix (DNS-safe: lowercase letters and numbers)
	randomSuffix := generateRandomSuffix(5)
	return fmt.Sprintf("%s-%s-%s-%s", name, backend, suffix, randomSuffix)
}

// generateRandomSuffix generates a random lowercase hex string of specified length
// Uses timestamp as seed to ensure uniqueness across different runs
func generateRandomSuffix(length int) string {
	// Use current timestamp (nanoseconds) as seed for randomness
	source := rand.NewSource(time.Now().UnixNano())
	rng := rand.New(source)

	// Calculate how many random bytes we need (2 hex chars per byte)
	bytes := make([]byte, (length+1)/2)
	for i := range bytes {
		bytes[i] = byte(rng.Intn(256))
	}

	hexString := hex.EncodeToString(bytes)
	return hexString[:length]
}

// getModelPath determines the model path based on HuggingFace ID or model name
func getModelPath(modelName, hfID string) string {
	if hfID != "" {
		// Use HuggingFace ID if provided
		parts := strings.Split(hfID, "/")
		if len(parts) > 0 {
			return fmt.Sprintf("/models/%s/", parts[len(parts)-1])
		}
	}
	// Fallback to model name
	return fmt.Sprintf("/models/%s/", modelName)
}

// getImage selects the appropriate container image
func getImage(backend string) string {
	// Default images per backend
	switch backend {
	case BackendSGLang:
		return "lmsysorg/sglang:latest"
	case BackendVLLM:
		return "vllm/vllm-openai:latest"
	case BackendTRTLLM:
		return "nvcr.io/nvidia/ai-dynamo/tensorrtllm-runtime:latest"
	default:
		return "lmsysorg/sglang:latest"
	}
}

// marshalMultiDocYAML marshals multiple documents into a YAML string
// Handles both regular Kubernetes objects and ApplyConfiguration objects
func marshalMultiDocYAML(docs ...interface{}) (string, error) {
	var result strings.Builder

	for i, doc := range docs {
		if i > 0 {
			result.WriteString("---\n")
		}

		// Convert ApplyConfiguration to unstructured format
		unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(doc)
		if err != nil {
			return "", fmt.Errorf("failed to convert document %d to unstructured: %w", i, err)
		}

		// Marshal to YAML using yaml.v2
		yamlBytes, err := yaml.Marshal(unstructuredObj)
		if err != nil {
			return "", fmt.Errorf("failed to marshal document %d: %w", i, err)
		}

		result.Write(yamlBytes)
	}

	return result.String(), nil
}
