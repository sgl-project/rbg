/*
Copyright 2026.

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

package util

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// MaskType defines how to mask sensitive input during display
type MaskType int

const (
	// MaskNone shows the input as-is
	MaskNone MaskType = iota
	// MaskPrevious shows only the last character, masking all previous characters
	// Example: 'abcd' displays as '***d', 'a' displays as 'a'
	MaskPrevious
	// MaskAll displays nothing to avoid revealing password length
	// Example: 'abcd' displays as '' (empty)
	MaskAll
)

// ConfigField describes a single configuration key for a plugin.
type ConfigField struct {
	Key         string
	Description string
	Required    bool
	// Masked defines how to mask the input during display for sensitive fields
	Masked MaskType
}

// ValidateConfig checks that all required fields are present and that no unknown fields are provided.
func ValidateConfig(fields []ConfigField, config map[string]interface{}) error {
	// Build a set of valid keys
	validKeys := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		validKeys[f.Key] = struct{}{}
	}

	// Check for unknown keys
	for key := range config {
		if _, ok := validKeys[key]; !ok {
			return fmt.Errorf("unknown config field %q", key)
		}
	}

	// Check required fields
	for _, f := range fields {
		if !f.Required {
			continue
		}
		v, ok := config[f.Key]
		if !ok || fmt.Sprintf("%v", v) == "" {
			return fmt.Errorf("required config field %q is missing (hint: %s)", f.Key, f.Description)
		}
	}
	return nil
}

// ReadLine reads a line from stdin with prompt
func ReadLine(reader *bufio.Reader, prompt string, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	_ = os.Stdout.Sync() // Flush the prompt
	line, err := reader.ReadString('\n')
	if err != nil {
		// EOF or error, return default
		return defaultValue
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}

// ReadMaskedLine reads a line from stdin with masking for sensitive input
func ReadMaskedLine(prompt string, defaultValue string, maskType MaskType) string {
	if defaultValue != "" {
		// Always mask all for default value display
		fmt.Printf("%s [%s]: ", prompt, MaskValue(defaultValue, MaskAll))
	} else {
		fmt.Printf("%s: ", prompt)
	}
	_ = os.Stdout.Sync() // Flush the prompt

	// Check if stdin is a terminal
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Not a terminal, read normally without masking
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil {
			return defaultValue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultValue
		}
		return line
	}

	// Terminal mode: read character by character with masking
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return defaultValue
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

	var input strings.Builder
	buf := make([]byte, 1)

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			break
		}

		ch := buf[0]
		switch ch {
		case '\r', '\n':
			// Enter pressed
			fmt.Print("\r\n")
			if input.Len() == 0 {
				return defaultValue
			}
			return input.String()
		case 127, 8: // Backspace (127 on Unix, 8 on some systems)
			if input.Len() > 0 {
				// Remove last character
				inputStr := input.String()
				input.Reset()
				input.WriteString(inputStr[:len(inputStr)-1])
				// Clear line and reprint masked value
				fmt.Print("\r\033[K") // Clear line
				fmt.Printf("%s: ", prompt)
				if input.Len() > 0 {
					// Use MaskAll after deletion to not reveal the new last char
					fmt.Print(MaskValue(input.String(), MaskAll))
				}
			}
		case 3: // Ctrl+C
			fmt.Print("\r\n")
			// Restore terminal state before exit
			_ = term.Restore(int(os.Stdin.Fd()), oldState)
			// Exit with SIGINT exit code (128 + signal number)
			os.Exit(128 + int(syscall.SIGINT))
		default:
			// Regular character
			if ch >= 32 && ch < 127 { // Printable ASCII
				input.WriteByte(ch)
				// Clear and reprint entire masked string
				fmt.Print("\r\033[K") // Clear line
				fmt.Printf("%s: ", prompt)
				fmt.Print(MaskValue(input.String(), maskType))
			}
		}
	}

	if input.Len() == 0 {
		return defaultValue
	}
	return input.String()
}

// MaskValue returns the masked representation of a value based on maskType
func MaskValue(value string, maskType MaskType) string {
	if len(value) == 0 {
		return ""
	}
	switch maskType {
	case MaskNone:
		return value
	case MaskPrevious:
		// Show last character (including when only 1 char)
		if len(value) == 1 {
			return value
		}
		return strings.Repeat("*", len(value)-1) + string(value[len(value)-1])
	case MaskAll:
		// Return empty string to not reveal password length
		return ""
	default:
		return value
	}
}

// MaskedDisplay returns a fixed-length masked string for display purposes
// This is used when showing config values to avoid revealing the actual length
func MaskedDisplay() string {
	return "******"
}
