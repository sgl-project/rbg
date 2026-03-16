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
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// chatMessage represents one turn in a conversation.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the OpenAI-compatible /v1/chat/completions request body.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// chatClient sends requests to an OpenAI-compatible inference endpoint.
type chatClient struct {
	baseURL    string
	modelName  string
	httpClient *http.Client
}

// newChatClient creates a client targeting the given base URL.
// modelName is the served-model-name passed to the engine (matches --served-model-name / --model-name arg).
// requestTimeout controls how long the HTTP client waits for a complete response.
func newChatClient(baseURL, modelName string, requestTimeout time.Duration) *chatClient {
	return &chatClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		modelName: modelName,
		httpClient: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

// sendNonStreaming sends a single prompt and returns the full response text.
func (c *chatClient) sendNonStreaming(history []chatMessage) (string, error) {
	reqBody := chatRequest{
		Model:    c.modelName,
		Messages: history,
		Stream:   false,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(c.baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

// sendStreaming sends a request with streaming enabled and writes each token
// chunk to out as it arrives. It returns the full assembled response.
func (c *chatClient) sendStreaming(history []chatMessage, out io.Writer) (string, error) {
	reqBody := chatRequest{
		Model:    c.modelName,
		Messages: history,
		Stream:   true,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(c.baseURL+"/v1/chat/completions", "application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			token := chunk.Choices[0].Delta.Content
			sb.WriteString(token)
			fmt.Fprint(out, token)
		}
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), fmt.Errorf("reading stream: %w", err)
	}
	return sb.String(), nil
}
