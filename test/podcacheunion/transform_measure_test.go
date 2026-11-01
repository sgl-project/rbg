//go:build podcacheunion

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

package podcacheunion

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientfeatures "k8s.io/client-go/features"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// transformResult is one cache process of the transform comparison. Peaks are
// derived from the native samples by the summary, so raw data stays auditable.
type transformResult struct {
	Mode               string         `json:"mode"`
	WatchListClient    bool           `json:"watchListClient"`
	APIServerGates     string         `json:"apiServerFeatureGates"`
	Round              int            `json:"round"`
	RelatedPods        int            `json:"relatedPods"`
	UnrelatedPods      int            `json:"unrelatedPods"`
	StartUnixNano      int64          `json:"startUnixNano"`
	SyncUnixNano       int64          `json:"syncUnixNano"`
	SyncMillis         float64        `json:"syncMillis"`
	StartupCPUMillis   float64        `json:"startupCPUMillis"`
	StartupBytes       int64          `json:"startupResponseBytes"`
	WindowEnd          memorySample   `json:"windowEnd"`
	PostGC             memorySample   `json:"postGC"`
	PreRelist          *memorySample  `json:"preRelist,omitempty"`
	RelistTriggerNano  int64          `json:"relistTriggerUnixNano,omitempty"`
	RelistStartNano    int64          `json:"relistStartUnixNano,omitempty"`
	RelistDoneNano     int64          `json:"relistDoneUnixNano,omitempty"`
	RelistCPUMillis    float64        `json:"relistCPUMillis,omitempty"`
	RelistBytes        int64          `json:"relistResponseBytes,omitempty"`
	PostRelistGC       *memorySample  `json:"postRelistGC,omitempty"`
	NativeSamples      []nativeSample `json:"nativeSamples"`
	Lists              int            `json:"lists"`
	Watches            int            `json:"watches"`
	StreamingLists     int            `json:"streamingLists"`
	InitialEventsEnds  int            `json:"initialEventsEnds"`
	RetainedPods       int            `json:"retainedPods"`
	FullPods           int            `json:"fullPods"`
	StubPods           int            `json:"stubPods"`
	FullManagedFields  int            `json:"fullPodsWithManagedFields"`
	MaxConcurrentLists int            `json:"maxConcurrentLists,omitempty"`
}

func processCPU(t *testing.T) time.Duration {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func apiServerStreams(gates string) bool {
	return strings.Contains(gates, "WatchList=true")
}

// TestTransformProbeChild measures one cache mode in its own process: startup
// to sync plus two seconds without explicit GC, retained heap after two GCs,
// then an injected 410 and the relist that follows.
func TestTransformProbeChild(t *testing.T) {
	mode := os.Getenv("RBG_POD_UNION_TRANSFORM_MODE")
	if mode == "" {
		t.Skip("opt-in transform comparison subprocess")
	}
	base, streaming := strings.CutSuffix(mode, "+wl")
	if streaming != (os.Getenv("KUBE_FEATURE_WatchListClient") == "true") {
		t.Fatalf("mode %s does not match KUBE_FEATURE_WatchListClient=%q", mode, os.Getenv("KUBE_FEATURE_WatchListClient"))
	}
	// TestMain sets the gate explicitly; the environment variable alone is
	// ineffective with the production imports linked into this binary.
	if got := clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient); got != streaming {
		t.Fatalf("WatchListClient gate reads %v after Set(%t)", got, streaming)
	}
	counter := &responseCounter{}
	var err error
	apiConfig, err = clientcmd.BuildConfigFromFlags("", os.Getenv("RBG_POD_UNION_TEST_KUBECONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	apiConfig.QPS, apiConfig.Burst = 200, 300
	apiConfig.Wrap(counter.wrap)
	apiClient, err = client.New(apiConfig, client.Options{Scheme: probeScheme})
	if err != nil {
		t.Fatal(err)
	}
	ns := os.Getenv("RBG_POD_UNION_TEST_NAMESPACE")
	gates := os.Getenv("RBG_POD_UNION_APISERVER_FEATURE_GATES")
	result := transformResult{
		Mode: mode, WatchListClient: streaming, APIServerGates: gates,
		Round:       envInt(t, "RBG_POD_UNION_TRANSFORM_ROUND", 0),
		RelatedPods: envInt(t, "RBG_POD_UNION_RELATED_PODS", 900), UnrelatedPods: envInt(t, "RBG_POD_UNION_UNRELATED_PODS", 100),
	}
	podOptions := cache.ByObject{Field: fields.OneTermEqualSelector("metadata.namespace", ns)}
	options := cache.Options{Scheme: probeScheme}
	relist := newRelistProbe()
	var probe *Probe
	switch base {
	case "full":
		options.NewInformer = relist.NewInformer
	case "union-serial":
		if streaming {
			t.Fatal("the union prototype bridges list/watch itself and has no streaming mode")
		}
		probe = probeFor(t, ns)
		probe.AggregateLists, probe.SerialInitialLists = true, true
		options.NewInformer = probe.NewInformer
		relist = nil
	case "transform-identity":
		podOptions.Transform = PodCacheTransform(StubIdentity)
		options.NewInformer = relist.NewInformer
	case "transform-labels-owners":
		podOptions.Transform = PodCacheTransform(StubIdentityLabelsOwners)
		options.NewInformer = relist.NewInformer
	case "transform-identity-stream":
		// Also transform watch events before the reflector's streaming-list
		// temporary store sees them; see WatchStreamTransformInformer.
		podOptions.Transform = PodCacheTransform(StubIdentity)
		options.NewInformer = streamTransformRelistInformer(relist, PodCacheTransform(StubIdentity))
	case "transform-labels-owners-stream":
		podOptions.Transform = PodCacheTransform(StubIdentityLabelsOwners)
		options.NewInformer = streamTransformRelistInformer(relist, PodCacheTransform(StubIdentityLabelsOwners))
	default:
		t.Fatalf("unknown transform comparison mode %q", mode)
	}
	options.ByObject = map[client.Object]cache.ByObject{&corev1.Pod{}: podOptions}
	c, informer, ctx, cancel := newCacheWithOptions(t, options)

	root := os.Getenv("RBG_POD_UNION_TRANSFORM_OUTPUT")
	nativePath := root + ".native.csv"
	nativeFile, err := os.Create(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	defer nativeFile.Close()
	sampler := exec.Command(os.Getenv("RBG_POD_UNION_NATIVE_SAMPLER"), strconv.Itoa(os.Getpid()))
	sampler.Stdout, sampler.Stderr = nativeFile, os.Stderr
	if err := sampler.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sampler.ProcessState == nil {
			_ = sampler.Process.Signal(syscall.SIGTERM)
			_ = sampler.Wait()
		}
	}()

	cpuStart := processCPU(t)
	start := time.Now()
	result.StartUnixNano = start.UnixNano()
	runCache(t, c, ctx, cancel)
	// Poll at 5 ms; WaitForCacheSync polls at 100 ms, too coarse to compare modes.
	for !informer.HasSynced() {
		if time.Since(start) > time.Minute {
			t.Fatal("initial cache sync timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
	synced := time.Now()
	result.SyncUnixNano = synced.UnixNano()
	result.SyncMillis = millis(synced.Sub(start))
	result.StartupCPUMillis = millis(processCPU(t) - cpuStart)
	result.StartupBytes = counter.bytes.Load()
	waitSync(t, c, ctx)
	time.Sleep(time.Until(synced.Add(2 * time.Second)))
	result.WindowEnd = captureMemorySample(t)
	runtime.GC()
	runtime.GC()
	result.PostGC = captureMemorySample(t)

	if relist != nil {
		time.Sleep(time.Second)
		pre := captureMemorySample(t)
		result.PreRelist = &pre
		cpuBefore, bytesBefore := processCPU(t), counter.bytes.Load()
		trigger := time.Now()
		result.RelistTriggerNano = trigger.UnixNano()
		if relist.expireWatches() == 0 {
			t.Fatal("no open watch to expire")
		}
		var started, done time.Time
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			var ok bool
			if started, done, ok = relist.relistCompletion(trigger); ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("relist after the injected 410 did not complete")
			}
		}
		result.RelistStartNano, result.RelistDoneNano = started.UnixNano(), done.UnixNano()
		time.Sleep(time.Until(done.Add(2 * time.Second)))
		result.RelistCPUMillis = millis(processCPU(t) - cpuBefore)
		result.RelistBytes = counter.bytes.Load() - bytesBefore
		runtime.GC()
		runtime.GC()
		post := captureMemorySample(t)
		result.PostRelistGC = &post
	}

	if err := sampler.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := sampler.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := nativeFile.Close(); err != nil {
		t.Fatal(err)
	}
	result.NativeSamples = readNativeSamples(t, nativePath)

	// Inspect the store only after sampling so accounting cannot skew memory.
	for _, obj := range informer.GetIndexer().List() {
		p := obj.(*corev1.Pod)
		result.RetainedPods++
		if isStub(p) {
			result.StubPods++
			continue
		}
		result.FullPods++
		if p.ManagedFields != nil {
			result.FullManagedFields++
		}
	}
	if relist != nil {
		result.Lists, result.Watches, result.StreamingLists, result.InitialEventsEnds = relist.counts()
	}
	if probe != nil {
		probe.mu.Lock()
		result.MaxConcurrentLists = probe.maxActiveLists
		probe.mu.Unlock()
	}

	total := result.RelatedPods + result.UnrelatedPods
	switch base {
	case "full":
		if result.RetainedPods != total || result.FullPods != total {
			t.Fatalf("full cache retained %d Pods (%d whole), want %d", result.RetainedPods, result.FullPods, total)
		}
	case "union-serial":
		if result.RetainedPods != result.RelatedPods || result.MaxConcurrentLists != 1 {
			t.Fatalf("union retained %d Pods with %d concurrent Lists", result.RetainedPods, result.MaxConcurrentLists)
		}
	default:
		if result.RetainedPods != total || result.FullPods != result.RelatedPods || result.StubPods != result.UnrelatedPods || result.FullManagedFields != 0 {
			t.Fatalf("transform retained %d Pods: %d whole (%d with managedFields), %d stubs", result.RetainedPods, result.FullPods, result.FullManagedFields, result.StubPods)
		}
	}
	if relist != nil {
		switch {
		case streaming && apiServerStreams(gates):
			// Initial sync and relist both streamed; the reflector never listed.
			if result.Lists != 0 || result.StreamingLists < 2 || result.InitialEventsEnds < 2 {
				t.Fatalf("expected streaming list for sync and relist: lists=%d streaming=%d initialEventsEnd=%d", result.Lists, result.StreamingLists, result.InitialEventsEnds)
			}
		case streaming:
			// The server rejects streaming; the reflector falls back to List.
			if result.Lists < 2 || result.StreamingLists < 2 || result.InitialEventsEnds != 0 {
				t.Fatalf("expected streaming attempts with List fallback: lists=%d streaming=%d initialEventsEnd=%d", result.Lists, result.StreamingLists, result.InitialEventsEnds)
			}
		default:
			if result.Lists < 2 || result.StreamingLists != 0 {
				t.Fatalf("expected List for sync and relist: lists=%d streaming=%d", result.Lists, result.StreamingLists)
			}
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+".json", append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(c)
	runtime.KeepAlive(probe)
	t.Logf("mode=%s sync=%.1fms retained=%d whole=%d stubs=%d lists=%d streaming=%d", mode, result.SyncMillis, result.RetainedPods, result.FullPods, result.StubPods, result.Lists, result.StreamingLists)
}

// TestAPITransformComparison runs every cache mode of one round against one
// isolated API server and dataset, one process at a time.
func TestAPITransformComparison(t *testing.T) {
	if os.Getenv("RBG_POD_UNION_TRANSFORM_COMPARISON") != "1" {
		t.Skip("opt-in transform memory comparison")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("the native memory sampler supports macOS and Linux only")
	}
	related := envInt(t, "RBG_POD_UNION_RELATED_PODS", 900)
	unrelated := envInt(t, "RBG_POD_UNION_UNRELATED_PODS", 100)
	ns := namespace(t)
	for i := range related + unrelated {
		values := map[string]string{"app": "unrelated"}
		if i < related {
			values = map[string]string{groupLabel: "g", instanceLabel: "i", warmupLabel: "w", managedLabel: "true"}
		}
		p := pod(ns, fmt.Sprintf("transform-%05d", i), values)
		for index := range 64 {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: fmt.Sprintf("DATA_%03d", index), Value: strings.Repeat("x", 512)})
		}
		create(t, p)
	}
	config := clientcmdapi.NewConfig()
	config.Clusters["probe"] = &clientcmdapi.Cluster{Server: apiConfig.Host, CertificateAuthority: apiConfig.CAFile, CertificateAuthorityData: apiConfig.CAData}
	config.AuthInfos["probe"] = &clientcmdapi.AuthInfo{ClientCertificate: apiConfig.CertFile, ClientCertificateData: apiConfig.CertData, ClientKey: apiConfig.KeyFile, ClientKeyData: apiConfig.KeyData}
	config.Contexts["probe"] = &clientcmdapi.Context{Cluster: "probe", AuthInfo: "probe"}
	config.CurrentContext = "probe"
	temp := t.TempDir()
	configPath := filepath.Join(temp, "isolated.kubeconfig")
	if err := clientcmd.WriteToFile(*config, configPath); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	samplerPath := filepath.Join(temp, "native-sampler")
	compiler := "clang"
	if runtime.GOOS == "linux" {
		compiler = "cc"
	}
	compile := exec.Command(compiler, "-O2", "-Wall", "-Wextra", "-Werror", "-o", samplerPath, filepath.Join(filepath.Dir(file), "testdata/memory_sampler_"+runtime.GOOS+".c"))
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("native sampler build: %v\n%s", err, output)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outputDir := os.Getenv("RBG_POD_UNION_TRANSFORM_DIR")
	if outputDir == "" {
		outputDir = temp
	}
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	gates := os.Getenv("RBG_POD_UNION_APISERVER_FEATURE_GATES")
	// Measure the selected wrapper with streaming off, fallback, and streaming
	// on. Keep the store-only transform as a control for the streaming peak.
	modes := []string{"full", "union-serial", "transform-labels-owners-stream", "transform-labels-owners-stream+wl"}
	if apiServerStreams(gates) {
		modes = []string{"full", "full+wl", "transform-labels-owners+wl", "transform-labels-owners-stream+wl"}
	}
	if override := os.Getenv("RBG_POD_UNION_TRANSFORM_MODES"); override != "" {
		modes = strings.Split(override, ",")
	}
	round := envInt(t, "RBG_POD_UNION_TRANSFORM_ROUND", 0)
	rotation := round % len(modes)
	modes = append(modes[rotation:], modes[:rotation]...)
	results := make([]transformResult, 0, len(modes))
	for _, mode := range modes {
		root := filepath.Join(outputDir, strings.ReplaceAll(mode, "+", "-"))
		log, err := os.OpenFile(root+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		streaming := "false"
		if strings.HasSuffix(mode, "+wl") {
			streaming = "true"
		}
		command := exec.Command(binary, "-test.run=^TestTransformProbeChild$", "-test.v", "-test.timeout=5m")
		command.Env = append(os.Environ(),
			"RBG_POD_UNION_TRANSFORM_MODE="+mode, "KUBE_FEATURE_WatchListClient="+streaming,
			"RBG_POD_UNION_TEST_KUBECONFIG="+configPath, "RBG_POD_UNION_TEST_NAMESPACE="+ns,
			"RBG_POD_UNION_NATIVE_SAMPLER="+samplerPath, "RBG_POD_UNION_TRANSFORM_OUTPUT="+root,
			fmt.Sprintf("RBG_POD_UNION_RELATED_PODS=%d", related), fmt.Sprintf("RBG_POD_UNION_UNRELATED_PODS=%d", unrelated))
		command.Stdout, command.Stderr = log, log
		// Processes run one at a time so no startup, GC, or relist overlaps.
		runErr := command.Run()
		_ = log.Close()
		if runErr != nil {
			t.Fatalf("%s failed: %v; log=%s.log", mode, runErr, root)
		}
		data, err := os.ReadFile(root + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var result transformResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
		t.Logf("%s: sync %.1f ms, retained heap %.2f MiB, peak RSS %.2f MiB", mode, result.SyncMillis, mib(result.PostGC.HeapAllocBytes), mib(uint64(result.WindowEnd.HistoricalPeakRSSBytes)))
		time.Sleep(2 * time.Second)
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "results.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func mib(bytes uint64) float64 {
	return float64(bytes) / (1 << 20)
}

// transformMetrics derives the reported figures from one raw result.
type transformMetrics struct {
	RetainedHeapMiB       float64
	PeakRSSMiB            float64
	PeakFootprintMiB      float64
	SyncMillis            float64
	StartupCPUMillis      float64
	StartupResponseMiB    float64
	RelistMillis          float64
	RelistPeakFootprint   float64
	RelistFootprintGrowth float64
	RelistCPUMillis       float64
	RelistResponseMiB     float64
	HasRelist             bool
}

func deriveTransformMetrics(r transformResult) transformMetrics {
	m := transformMetrics{
		RetainedHeapMiB:    mib(r.PostGC.HeapAllocBytes),
		PeakRSSMiB:         mib(uint64(r.WindowEnd.HistoricalPeakRSSBytes)),
		SyncMillis:         r.SyncMillis,
		StartupCPUMillis:   r.StartupCPUMillis,
		StartupResponseMiB: mib(uint64(r.StartupBytes)),
	}
	var before, peakFootprint, relistPeak uint64
	for _, s := range r.NativeSamples {
		if s.UnixNano <= r.WindowEnd.UnixNano && s.HistoricalFootprintPeakBytes > peakFootprint {
			peakFootprint = s.HistoricalFootprintPeakBytes
		}
		if r.RelistTriggerNano == 0 {
			continue
		}
		if s.UnixNano <= r.RelistTriggerNano {
			before = s.PhysicalFootprintBytes
		} else if s.UnixNano <= r.RelistDoneNano+int64(2*time.Second) && s.PhysicalFootprintBytes > relistPeak {
			relistPeak = s.PhysicalFootprintBytes
		}
	}
	m.PeakFootprintMiB = mib(peakFootprint)
	if r.RelistTriggerNano != 0 {
		m.HasRelist = true
		m.RelistMillis = float64(r.RelistDoneNano-r.RelistStartNano) / 1e6
		m.RelistPeakFootprint = mib(relistPeak)
		m.RelistFootprintGrowth = mib(relistPeak) - mib(before)
		m.RelistCPUMillis = r.RelistCPUMillis
		m.RelistResponseMiB = mib(uint64(r.RelistBytes))
	}
	return m
}

func medianRange(values []float64) (float64, float64, float64) {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return sorted[len(sorted)/2], sorted[0], sorted[len(sorted)-1]
}

// TestTransformComparisonSummary reads every results.json below a directory and
// prints the median and range per server mode, dataset shape, and cache mode.
func TestTransformComparisonSummary(t *testing.T) {
	dir := os.Getenv("RBG_POD_UNION_TRANSFORM_SUMMARY_DIR")
	if dir == "" {
		t.Skip("set RBG_POD_UNION_TRANSFORM_SUMMARY_DIR to summarize saved runs")
	}
	type key struct {
		streams            bool
		related, unrelated int
		mode               string
	}
	groups := map[key][]transformMetrics{}
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Name() != "results.json" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var results []transformResult
		if err := json.Unmarshal(data, &results); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, r := range results {
			k := key{apiServerStreams(r.APIServerGates), r.RelatedPods, r.UnrelatedPods, r.Mode}
			groups[k] = append(groups[k], deriveTransformMetrics(r))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	modeOrder := map[string]int{"full": 0, "full+wl": 1, "union-serial": 2, "transform-labels-owners": 3, "transform-labels-owners-stream": 4, "transform-labels-owners+wl": 5, "transform-labels-owners-stream+wl": 6, "transform-identity": 7, "transform-identity+wl": 8, "transform-identity-stream+wl": 9}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.streams != b.streams {
			return !a.streams
		}
		if a.unrelated != b.unrelated {
			return a.unrelated > b.unrelated
		}
		if a.related != b.related {
			return a.related < b.related
		}
		return modeOrder[a.mode] < modeOrder[b.mode]
	})
	format := func(values []float64, precision int) string {
		median, low, high := medianRange(values)
		return fmt.Sprintf("%.*f [%.*f–%.*f]", precision, median, precision, low, precision, high)
	}
	var out strings.Builder
	out.WriteString("| Server streaming | Related / unrelated | Mode | Runs | Retained heap (MiB) | Peak RSS (MiB) | Peak footprint (MiB) | Sync (ms) | Startup CPU (ms) | Startup response (MiB) |\n")
	out.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, k := range keys {
		ms := groups[k]
		column := func(pick func(transformMetrics) float64) []float64 {
			values := make([]float64, 0, len(ms))
			for _, m := range ms {
				values = append(values, pick(m))
			}
			return values
		}
		fmt.Fprintf(&out, "| %v | %d / %d | %s | %d | %s | %s | %s | %s | %s | %s |\n", k.streams, k.related, k.unrelated, k.mode, len(ms),
			format(column(func(m transformMetrics) float64 { return m.RetainedHeapMiB }), 2),
			format(column(func(m transformMetrics) float64 { return m.PeakRSSMiB }), 2),
			format(column(func(m transformMetrics) float64 { return m.PeakFootprintMiB }), 1),
			format(column(func(m transformMetrics) float64 { return m.SyncMillis }), 0),
			format(column(func(m transformMetrics) float64 { return m.StartupCPUMillis }), 0),
			format(column(func(m transformMetrics) float64 { return m.StartupResponseMiB }), 1))
	}
	out.WriteString("\n| Server streaming | Related / unrelated | Mode | Relist (ms) | Relist peak footprint (MiB) | Footprint growth during relist (MiB) | Relist CPU (ms) | Relist response (MiB) |\n")
	out.WriteString("| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, k := range keys {
		ms := groups[k]
		if !ms[0].HasRelist {
			continue
		}
		column := func(pick func(transformMetrics) float64) []float64 {
			values := make([]float64, 0, len(ms))
			for _, m := range ms {
				values = append(values, pick(m))
			}
			return values
		}
		fmt.Fprintf(&out, "| %v | %d / %d | %s | %s | %s | %s | %s | %s |\n", k.streams, k.related, k.unrelated, k.mode,
			format(column(func(m transformMetrics) float64 { return m.RelistMillis }), 0),
			format(column(func(m transformMetrics) float64 { return m.RelistPeakFootprint }), 1),
			format(column(func(m transformMetrics) float64 { return m.RelistFootprintGrowth }), 1),
			format(column(func(m transformMetrics) float64 { return m.RelistCPUMillis }), 0),
			format(column(func(m transformMetrics) float64 { return m.RelistResponseMiB }), 1))
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(out.String()), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + out.String())
}
