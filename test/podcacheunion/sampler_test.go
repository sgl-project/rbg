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
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// memorySample is a Go runtime snapshot taken inside a cache process. The JSON
// names are part of the saved results format.
type memorySample struct {
	UnixNano                int64  `json:"unixNano"`
	HeapAllocBytes          uint64 `json:"heapAllocBytes"`
	HeapInuseBytes          uint64 `json:"heapInuseBytes"`
	HeapIdleBytes           uint64 `json:"heapIdleBytes"`
	HeapReleasedBytes       uint64 `json:"heapReleasedBytes"`
	IdleNotReleasedBytes    uint64 `json:"idleNotReleasedBytes"`
	RuntimeSysMinusReleased uint64 `json:"runtimeSysMinusReleasedBytes"`
	TotalAllocBytes         uint64 `json:"totalAllocBytes"`
	NextGCBytes             uint64 `json:"nextGCBytes"`
	NumGC                   uint32 `json:"numGC"`
	LastGCUnixNano          uint64 `json:"lastGCUnixNano"`
	HistoricalPeakRSSBytes  int64  `json:"historicalPeakRSSBytes"`
}

// nativeSample is one row of the out-of-process sampler
// (testdata/memory_sampler_<GOOS>.c).
type nativeSample struct {
	UnixNano                     int64  `json:"unixNano"`
	RSSBytes                     uint64 `json:"rssBytes"`
	PhysicalFootprintBytes       uint64 `json:"physicalFootprintBytes"`
	HistoricalFootprintPeakBytes uint64 `json:"historicalFootprintPeakBytes"`
}

func captureMemorySample(t *testing.T) memorySample {
	t.Helper()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	// ru_maxrss is in bytes on macOS and in KiB on Linux.
	peak := usage.Maxrss
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	return memorySample{
		UnixNano: time.Now().UnixNano(), HeapAllocBytes: stats.HeapAlloc,
		HeapInuseBytes: stats.HeapInuse, HeapIdleBytes: stats.HeapIdle,
		HeapReleasedBytes: stats.HeapReleased, IdleNotReleasedBytes: stats.HeapIdle - stats.HeapReleased,
		RuntimeSysMinusReleased: stats.Sys - stats.HeapReleased,
		TotalAllocBytes:         stats.TotalAlloc, NextGCBytes: stats.NextGC,
		NumGC: stats.NumGC, LastGCUnixNano: stats.LastGC, HistoricalPeakRSSBytes: peak,
	}
}

func readNativeSamples(t *testing.T, path string) []nativeSample {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result []nativeSample
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row nativeSample
		if _, err := fmt.Sscanf(line, "%d,%d,%d,%d", &row.UnixNano, &row.RSSBytes, &row.PhysicalFootprintBytes, &row.HistoricalFootprintPeakBytes); err != nil {
			t.Fatalf("native sampler row %q: %v", line, err)
		}
		if row.RSSBytes == 0 || row.PhysicalFootprintBytes == 0 || row.HistoricalFootprintPeakBytes == 0 {
			t.Fatalf("invalid native memory accounting: %+v", row)
		}
		result = append(result, row)
	}
	return result
}

func envInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		t.Fatalf("invalid %s=%q", key, raw)
	}
	return value
}
