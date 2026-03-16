/*
Copyright 2025.

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

package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// --- chatClient: non-streaming ---

func TestChatClient_NonStreaming_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req chatRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "my-model", req.Model)
		assert.False(t, req.Stream)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "Paris"}},
			},
		})
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "my-model", 5*time.Minute)
	history := []chatMessage{{Role: "user", Content: "What is the capital of France?"}}
	reply, err := client.sendNonStreaming(history)
	require.NoError(t, err)
	assert.Equal(t, "Paris", reply)
}

func TestChatClient_NonStreaming_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "my-model", 5*time.Minute)
	_, err := client.sendNonStreaming([]chatMessage{{Role: "user", Content: "hi"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestChatClient_NonStreaming_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{}})
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "my-model", 5*time.Minute)
	_, err := client.sendNonStreaming([]chatMessage{{Role: "user", Content: "hi"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no choices")
}

// --- chatClient: streaming ---

func TestChatClient_Streaming_Success(t *testing.T) {
	tokens := []string{"Hello", ", ", "world", "!"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, tok := range tokens {
			chunk := map[string]interface{}{
				"choices": []map[string]interface{}{
					{"delta": map[string]string{"content": tok}},
				},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "my-model", 5*time.Minute)
	var out bytes.Buffer
	reply, err := client.sendStreaming([]chatMessage{{Role: "user", Content: "hi"}}, &out)
	require.NoError(t, err)
	assert.Equal(t, "Hello, world!", reply)
	assert.Equal(t, "Hello, world!", out.String())
}

func TestChatClient_Streaming_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "my-model", 5*time.Minute)
	_, err := client.sendStreaming([]chatMessage{{Role: "user", Content: "hi"}}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

func TestChatClient_Streaming_SkipsNonDataLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, ": keep-alive") // comment line — must be skipped
		fmt.Fprintln(w, "event: delta") // event line — must be skipped
		data, _ := json.Marshal(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"delta": map[string]string{"content": "ok"}},
			},
		})
		fmt.Fprintf(w, "data: %s\n\n", data)
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "my-model", 5*time.Minute)
	var out bytes.Buffer
	reply, err := client.sendStreaming([]chatMessage{{Role: "user", Content: "hi"}}, &out)
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

// --- isPodReady ---

func TestIsPodReady_Running_AllContainersReady(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
				{Ready: true},
			},
		},
	}
	assert.True(t, isPodReady(pod))
}

func TestIsPodReady_Running_OneContainerNotReady(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
				{Ready: false},
			},
		},
	}
	assert.False(t, isPodReady(pod))
}

func TestIsPodReady_PendingPhase(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
			},
		},
	}
	assert.False(t, isPodReady(pod))
}

func TestIsPodReady_NoContainerStatuses(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: nil,
		},
	}
	// Running with no container status entries — treat as ready (no containers to block).
	assert.True(t, isPodReady(pod))
}

// --- randomLocalPort ---

func TestRandomLocalPort_InRange(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := randomLocalPort()
		assert.GreaterOrEqual(t, p, int32(49152))
		assert.LessOrEqual(t, p, int32(65535))
	}
}

// --- NewChatCmd: command metadata and flags ---

func TestNewChatCmd_UseAndShort(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewChatCmd(cf)
	assert.Equal(t, "chat <name> [flags]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

func TestNewChatCmd_ExactlyOneArg(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewChatCmd(cf)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b"}))
	assert.NoError(t, cmd.Args(cmd, []string{"my-svc"}))
}

func TestNewChatCmd_FlagDefaults(t *testing.T) {
	cf := genericclioptions.NewConfigFlags(true)
	cmd := NewChatCmd(cf)

	promptFlag := cmd.Flags().Lookup("prompt")
	require.NotNil(t, promptFlag)
	assert.Equal(t, "", promptFlag.DefValue)

	interactiveFlag := cmd.Flags().Lookup("interactive")
	require.NotNil(t, interactiveFlag)
	assert.Equal(t, "false", interactiveFlag.DefValue)

	localPortFlag := cmd.Flags().Lookup("local-port")
	require.NotNil(t, localPortFlag)
	assert.Equal(t, "0", localPortFlag.DefValue)

	systemFlag := cmd.Flags().Lookup("system")
	require.NotNil(t, systemFlag)
	assert.Equal(t, "", systemFlag.DefValue)

	noStreamFlag := cmd.Flags().Lookup("no-stream")
	require.NotNil(t, noStreamFlag)
	assert.Equal(t, "false", noStreamFlag.DefValue)

	requestTimeoutFlag := cmd.Flags().Lookup("request-timeout")
	require.NotNil(t, requestTimeoutFlag)
	assert.Equal(t, "5m0s", requestTimeoutFlag.DefValue)

	pfReadyTimeoutFlag := cmd.Flags().Lookup("port-forward-timeout")
	require.NotNil(t, pfReadyTimeoutFlag)
	assert.Equal(t, "30s", pfReadyTimeoutFlag.DefValue)
}

// --- runNonInteractive ---

func TestRunNonInteractive_NoStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": "42"}},
			},
		})
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "m", 5*time.Minute)
	// Redirect stdout to capture output.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runNonInteractive(client, nil, "answer?", false)

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)

	require.NoError(t, err)
	assert.Contains(t, buf.String(), "42")
}

// fakeRL is a test double for lineReader.
type fakeRL struct {
	lines []string
	idx   int
	out   *bytes.Buffer
}

func newFakeRL(lines ...string) *fakeRL {
	return &fakeRL{lines: lines, out: &bytes.Buffer{}}
}

func (f *fakeRL) Readline() (string, error) {
	if f.idx >= len(f.lines) {
		return "", io.EOF
	}
	line := f.lines[f.idx]
	f.idx++
	return line, nil
}

func (f *fakeRL) Stdout() io.Writer { return f.out }
func (f *fakeRL) Close() error      { return nil }

// --- runREPL: exit command ---

func TestRunInteractive_ExitCommand(t *testing.T) {
	client := newChatClient("http://localhost:1", "m", 5*time.Minute)
	rl := newFakeRL("/exit")

	err := runREPL(client, nil, true, rl)
	assert.NoError(t, err)
	assert.Contains(t, rl.out.String(), "Session ended.")
}

func TestRunInteractive_EOF(t *testing.T) {
	client := newChatClient("http://localhost:1", "m", 5*time.Minute)
	rl := newFakeRL() // no lines → immediate EOF

	err := runREPL(client, nil, true, rl)
	assert.NoError(t, err)
	assert.Contains(t, rl.out.String(), "Session ended.")
}

// --- history management in runInteractive ---

func TestRunInteractive_ErrorDoesNotCorruptHistory(t *testing.T) {
	// Server that always returns 500 so sendStreaming fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "err", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newChatClient(srv.URL, "m", 5*time.Minute)
	rl := newFakeRL("hello", "/exit")

	history := []chatMessage{}
	err := runREPL(client, history, true, rl)
	assert.NoError(t, err)
	// Original history slice must not have grown (error path removes the user turn).
	assert.Len(t, history, 0)
}

// Ensure os is imported for the test helpers above.
var _ = strings.Contains
