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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chzyer/readline"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	llmmeta "sigs.k8s.io/rbgs/cmd/cli/cmd/llm/metadata"
	"sigs.k8s.io/rbgs/cmd/cli/util"
)

// spinner writes an animated thinking indicator to stderr while a goroutine is
// running. Call stop() to clear the line and unblock.
type spinner struct {
	stop chan struct{}
	done chan struct{}
}

// startSpinner starts a spinner on stderr with the given prefix label.
// Call s.stop() when the operation completes.
func startSpinner(label string) *spinner {
	s := &spinner{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	go func() {
		defer close(s.done)
		var once sync.Once
		for i := 0; ; i++ {
			select {
			case <-s.stop:
				// Clear the spinner line before returning.
				once.Do(func() { fmt.Fprintf(os.Stderr, "\r\033[K") })
				return
			case <-time.After(80 * time.Millisecond):
				fmt.Fprintf(os.Stderr, "\r%s %s", frames[i%len(frames)], label)
			}
		}
	}()
	return s
}

// stopSpinner stops the spinner and waits for its goroutine to exit.
func (s *spinner) stopSpinner() {
	close(s.stop)
	<-s.done
}

// NewChatCmd creates the 'llm chat' command.
func NewChatCmd(cf *genericclioptions.ConfigFlags) *cobra.Command {
	var (
		prompt         string
		localPort      int32
		system         string
		noStream       bool
		interactive    bool
		requestTimeout time.Duration
		pfReadyTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "chat <name> [flags]",
		Short: "Chat with a running LLM inference service",
		Long: `Connect to an LLM service created by 'kubectl rbg llm run' and send messages.

Without --prompt the command enters interactive mode: a persistent chat session
that accepts input line by line. Type '/exit' or press Ctrl+C to quit.

The command locates a ready pod belonging to the named service, establishes a
port-forward tunnel, and communicates over the OpenAI /v1/chat/completions API.`,
		Example: `  # Non-interactive: send a single prompt
  kubectl rbg llm chat my-qwen --prompt "What is the capital of France?"

  # Interactive session
  kubectl rbg llm chat my-qwen

  # Use a system prompt
  kubectl rbg llm chat my-qwen --system "You are a helpful assistant."

  # Specify a local port for the tunnel (default: random free port)
  kubectl rbg llm chat my-qwen --local-port 18000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			namespace := util.GetNamespace(cf)
			ctx := context.Background()

			// 1. Fetch the RBG and parse its CLI metadata annotation.
			rbgClient, err := util.GetRBGClient(cf)
			if err != nil {
				return fmt.Errorf("failed to connect to cluster: %w", err)
			}
			// rbg, err := rbgClient.WorkloadsV1alpha2().RoleBasedGroups(namespace).Get(ctx, name, metav1.GetOptions{})
			rbg, err := rbgClient.WorkloadsV1alpha1().RoleBasedGroups(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return fmt.Errorf("RoleBasedGroup %q not found in namespace %q: %w", name, namespace, err)
			}

			annotationVal, ok := rbg.Annotations[llmmeta.RunCommandMetadataAnnotationKey]
			if !ok {
				return fmt.Errorf("RoleBasedGroup %q has no CLI metadata annotation; was it created with 'kubectl rbg llm run'?", name)
			}
			var meta llmmeta.RunMetadata
			if err := json.Unmarshal([]byte(annotationVal), &meta); err != nil {
				return fmt.Errorf("failed to parse CLI metadata annotation: %w", err)
			}
			if meta.Port == 0 {
				return fmt.Errorf("service port is not recorded in metadata; please re-create the service with an updated 'kubectl rbg llm run'")
			}

			// 2. Find a ready pod for the service.
			k8sClient, err := util.GetK8SClientSet(cf)
			if err != nil {
				return fmt.Errorf("failed to create Kubernetes client: %w", err)
			}
			labelSelector := fmt.Sprintf(
				"rolebasedgroup.workloads.x-k8s.io/name=%s,rolebasedgroup.workloads.x-k8s.io/role=inference",
				name,
			)
			pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: labelSelector,
			})
			if err != nil {
				return fmt.Errorf("failed to list pods: %w", err)
			}

			podName := ""
			for i := range pods.Items {
				p := &pods.Items[i]
				if isPodReady(p) {
					podName = p.Name
					break
				}
			}
			if podName == "" {
				return fmt.Errorf("no ready pods found for service %q (selector: %s)", name, labelSelector)
			}

			// 3. Resolve the local port.
			if localPort == 0 {
				localPort = randomLocalPort()
			}

			// 4. Get kubeconfig path for the port-forward subprocess.
			kubeconfig := ""
			if cf.KubeConfig != nil {
				kubeconfig = *cf.KubeConfig
			}

			// 5. Start port-forward tunnel.
			fmt.Fprintf(os.Stderr, "Connecting to pod %s (port-forward %d → %d)...\n", podName, localPort, meta.Port)
			session, err := startPortForward(kubeconfig, namespace, podName, localPort, meta.Port, pfReadyTimeout)
			if err != nil {
				return fmt.Errorf("port-forward failed: %w", err)
			}
			defer session.Stop()
			fmt.Fprintf(os.Stderr, "Connected.\n\n")

			// 6. Build the HTTP client.
			baseURL := fmt.Sprintf("http://localhost:%d", localPort)
			client := newChatClient(baseURL, name, requestTimeout)

			// 7. Build conversation history (system prompt if provided).
			history := []chatMessage{}
			if system != "" {
				history = append(history, chatMessage{Role: "system", Content: system})
			}

			// 8. Dispatch to non-interactive or interactive mode.
			switch {
			case prompt != "" && !interactive:
				return runNonInteractive(client, history, prompt, !noStream)
			case interactive:
				if prompt != "" {
					// Pre-seed the first user turn and open the REPL.
					history = append(history, chatMessage{Role: "user", Content: prompt})
				}
				return runInteractive(client, history, !noStream)
			default:
				return fmt.Errorf("specify --prompt for a single query or -i / --interactive for a chat session")
			}
		},
	}

	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "Single prompt (non-interactive); combined with -i to seed the first turn")
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "Start an interactive chat session (REPL)")
	cmd.Flags().Int32Var(&localPort, "local-port", 0, "Local port for the tunnel (default: random)")
	cmd.Flags().StringVar(&system, "system", "", "System prompt prepended to every conversation")
	cmd.Flags().BoolVar(&noStream, "no-stream", false, "Disable streaming; wait for the full response")
	cmd.Flags().DurationVar(&requestTimeout, "request-timeout", 5*time.Minute, "HTTP request timeout for model inference (e.g. 10m for large models)")
	cmd.Flags().DurationVar(&pfReadyTimeout, "port-forward-timeout", 30*time.Second, "Timeout waiting for the port-forward tunnel to become ready")

	return cmd
}

// runNonInteractive sends a single prompt and prints the response.
func runNonInteractive(client *chatClient, history []chatMessage, prompt string, stream bool) error {
	history = append(history, chatMessage{Role: "user", Content: prompt})

	if stream {
		// Streaming already shows tokens as they arrive — no spinner needed.
		_, err := client.sendStreaming(history, os.Stdout)
		if err != nil {
			return fmt.Errorf("chat request failed: %w", err)
		}
		fmt.Println()
		return nil
	}

	// Non-streaming: show a spinner while waiting for the full response.
	s := startSpinner("Thinking…")
	defer s.stopSpinner()
	reply, err := client.sendNonStreaming(history)
	if err != nil {
		return fmt.Errorf("chat request failed: %w", err)
	}
	fmt.Println(reply)
	return nil
}

// lineReader is the interface readline.Instance satisfies, extracted for testing.
type lineReader interface {
	Readline() (string, error)
	Stdout() io.Writer
	Close() error
}

// runInteractive runs a persistent REPL until the user exits.
// It uses readline for proper terminal line-editing (backspace, arrows, history).
func runInteractive(client *chatClient, history []chatMessage, stream bool) error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "You: ",
		HistoryLimit:    100,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
	})
	if err != nil {
		return fmt.Errorf("failed to initialize readline: %w", err)
	}
	return runREPL(client, history, stream, rl)
}

// runREPL is the testable core of the interactive session.
func runREPL(client *chatClient, history []chatMessage, stream bool, rl lineReader) error {
	defer rl.Close()
	out := rl.Stdout()

	fmt.Fprintln(out, "Interactive chat session started. Type '/exit' or press Ctrl+C to quit.")
	fmt.Fprintln(out, strings.Repeat("─", 50))

	for {
		input, err := rl.Readline()
		if err != nil {
			// io.EOF = Ctrl+D, readline.ErrInterrupt = Ctrl+C
			if err == io.EOF || err == readline.ErrInterrupt {
				fmt.Fprintln(out, "\nSession ended.")
				return nil
			}
			return err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if strings.EqualFold(input, "/exit") || strings.EqualFold(input, "exit") {
			fmt.Fprintln(out, "Session ended.")
			return nil
		}

		history = append(history, chatMessage{Role: "user", Content: input})

		fmt.Fprint(out, "Assistant: ")
		var reply string
		var rerr error
		if stream {
			// Streaming writes tokens live — no spinner needed.
			reply, rerr = client.sendStreaming(history, out)
		} else {
			// Non-streaming: show a spinner while the model is thinking.
			s := startSpinner("Thinking…")
			defer s.stopSpinner()
			reply, rerr = client.sendNonStreaming(history)
			if rerr == nil {
				fmt.Fprint(out, reply)
			}
		}
		fmt.Fprintln(out)

		if rerr != nil {
			fmt.Fprintf(out, "Error: %v\n", rerr)
			// Remove the failed user turn so the conversation stays consistent.
			history = history[:len(history)-1]
			continue
		}

		// Append the assistant reply to keep context.
		history = append(history, chatMessage{Role: "assistant", Content: reply})
	}
}

// isPodReady returns true if the pod is in Running phase and all containers are ready.
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.ContainerStatuses {
		if !c.Ready {
			return false
		}
	}
	return true
}

// randomLocalPort picks a random local port in the ephemeral range 49152–65535.
func randomLocalPort() int32 {
	//nolint:gosec // non-cryptographic use
	return int32(49152 + rand.Intn(16383))
}
