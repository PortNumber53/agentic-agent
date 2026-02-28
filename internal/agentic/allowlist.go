package agentic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/term"
)

const allowlistFile = ".agentic_allowlist.json"

// ShellAllowlist manages regex-based rules for auto-approving shell commands.
type ShellAllowlist struct {
	mu       sync.RWMutex
	Rules    []AllowRule `json:"rules"`
	compiled []*regexp.Regexp
	// ReadLineFunc reads a full line of text (Enter required). Set by the REPL layer.
	ReadLineFunc func(prompt string) (string, error)
}

type AllowRule struct {
	Pattern     string `json:"pattern"`
	Description string `json:"description,omitempty"`
}

var Allowlist = &ShellAllowlist{}

func (al *ShellAllowlist) Load() {
	al.mu.Lock()
	defer al.mu.Unlock()

	b, err := os.ReadFile(allowlistFile)
	if err != nil {
		return
	}

	var rules []AllowRule
	if err := json.Unmarshal(b, &rules); err != nil {
		return
	}

	al.Rules = nil
	al.compiled = nil
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			fmt.Printf("%s[warning] Invalid allowlist pattern %q: %v%s\n", ColorError, r.Pattern, err, ColorReset)
			continue
		}
		al.Rules = append(al.Rules, r)
		al.compiled = append(al.compiled, re)
	}
}

func (al *ShellAllowlist) Save() error {
	al.mu.RLock()
	defer al.mu.RUnlock()

	b, err := json.MarshalIndent(al.Rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(allowlistFile, b, 0644)
}

// IsAllowed checks if a command matches any allowlist pattern.
func (al *ShellAllowlist) IsAllowed(command string) bool {
	al.mu.RLock()
	defer al.mu.RUnlock()

	for _, re := range al.compiled {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// Add appends a new regex pattern to the allowlist.
func (al *ShellAllowlist) Add(pattern, description string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid regex: %v", err)
	}

	al.mu.Lock()
	al.Rules = append(al.Rules, AllowRule{Pattern: pattern, Description: description})
	al.compiled = append(al.compiled, re)
	al.mu.Unlock()

	return al.Save()
}

// Remove deletes a rule by index (0-based).
func (al *ShellAllowlist) Remove(index int) error {
	al.mu.Lock()
	defer al.mu.Unlock()

	if index < 0 || index >= len(al.Rules) {
		return fmt.Errorf("index %d out of range (0-%d)", index, len(al.Rules)-1)
	}

	al.Rules = append(al.Rules[:index], al.Rules[index+1:]...)
	al.compiled = append(al.compiled[:index], al.compiled[index+1:]...)

	b, err := json.MarshalIndent(al.Rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(allowlistFile, b, 0644)
}

// List returns a formatted string of all allowlist rules.
func (al *ShellAllowlist) List() string {
	al.mu.RLock()
	defer al.mu.RUnlock()

	if len(al.Rules) == 0 {
		return "  (no rules — all shell commands require confirmation)"
	}

	var sb strings.Builder
	for i, r := range al.Rules {
		desc := r.Description
		if desc == "" {
			desc = "-"
		}
		sb.WriteString(fmt.Sprintf("  [%d] /%s/  %s\n", i, r.Pattern, desc))
	}
	return sb.String()
}

// CheckCommand checks whether a command is allowed. If not, it prompts the
// user with a single keypress (y/n/a). Returns true if execution should proceed.
func (al *ShellAllowlist) CheckCommand(command string) bool {
	if al.IsAllowed(command) {
		return true
	}

	fmt.Printf("%s[shell] Execute: %s%s\n", ColorUser, command, ColorReset)
	fmt.Printf("  [y]es / [n]o / [a]llow: ")

	for {
		key, err := al.readKey()
		if err != nil {
			fmt.Println()
			return false
		}

		switch key {
		case 'y', 'Y':
			fmt.Printf("%sy%s\n", ColorAgent, ColorReset)
			return true
		case 'n', 'N', 0: // 0 = EOF/error
			fmt.Printf("%sn%s\n", ColorAgent, ColorReset)
			return false
		case 'a', 'A':
			fmt.Printf("%sa%s\n", ColorAgent, ColorReset)
			return al.handleAddFromPrompt(command)
		default:
			// ignore other keys, keep waiting
		}
	}
}

func (al *ShellAllowlist) readKey() (rune, error) {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		// Fallback: read one byte without raw mode
		buf := make([]byte, 1)
		_, err := os.Stdin.Read(buf)
		return rune(buf[0]), err
	}
	defer term.Restore(fd, oldState)

	buf := make([]byte, 1)
	_, err = os.Stdin.Read(buf)
	if err != nil {
		return 0, err
	}
	return rune(buf[0]), nil
}

func (al *ShellAllowlist) handleAddFromPrompt(command string) bool {
	defaultPattern := "^" + regexp.QuoteMeta(command) + "$"

	var pattern string
	if al.ReadLineFunc != nil {
		var err error
		pattern, err = al.ReadLineFunc(fmt.Sprintf("  Regex pattern [%s]: ", defaultPattern))
		if err != nil {
			pattern = ""
		}
	} else {
		fmt.Printf("  Regex pattern [%s]: ", defaultPattern)
		reader := bufio.NewReader(os.Stdin)
		pattern, _ = reader.ReadString('\n')
	}

	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		pattern = defaultPattern
	}

	if err := al.Add(pattern, "added via prompt"); err != nil {
		fmt.Printf("%s[error] %v%s\n", ColorError, err, ColorReset)
		return false
	}
	fmt.Printf("%s[allowlist] Added pattern: %s%s\n", ColorSystem, pattern, ColorReset)
	return true
}
