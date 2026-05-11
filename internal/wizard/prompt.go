package wizard

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// IsTerminal returns true when stdout is connected to an interactive
// terminal. When false the wizard skips all prompts and uses defaults.
func IsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

var stdinReader = bufio.NewReader(os.Stdin)

func readLine() string {
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// YN prints a [Y/n] or [y/N] prompt and returns the user's choice.
// When acceptDefault is true (--yes mode) or the terminal is not
// interactive, it returns defaultYes without prompting.
func YN(question string, defaultYes, acceptDefault bool) bool {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	if acceptDefault || !IsTerminal() {
		fmt.Printf("%s %s → %s\n", question, hint, yesNo(defaultYes))
		return defaultYes
	}
	fmt.Printf("%s %s: ", question, hint)
	line := strings.ToLower(strings.TrimSpace(readLine()))
	switch line {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return defaultYes
	}
}

// Str prints a prompt with a default value and returns the user's
// input (or the default if empty / non-interactive).
func Str(question, defaultVal string, acceptDefault bool) string {
	if acceptDefault || !IsTerminal() {
		fmt.Printf("%s [%s] → %s\n", question, defaultVal, defaultVal)
		return defaultVal
	}
	fmt.Printf("%s [%s]: ", question, defaultVal)
	line := strings.TrimSpace(readLine())
	if line == "" {
		return defaultVal
	}
	return line
}

// Choice prints a numbered list and returns the 0-based index chosen.
// acceptDefault selects index 0 without prompting.
func Choice(question string, options []string, acceptDefault bool) int {
	fmt.Println(question)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i+1, opt)
	}
	if acceptDefault || !IsTerminal() {
		fmt.Printf("→ 1 (default)\n")
		return 0
	}
	fmt.Print("→ ")
	line := strings.TrimSpace(readLine())
	if line == "" {
		return 0
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(options) {
		return 0
	}
	return n - 1
}

// Step prints a section separator with a step label.
func Step(label string) {
	fmt.Printf("\n%s\n", label)
}

// Done prints a ✓ confirmation line.
func Done(msg string) {
	fmt.Printf("  ✓ %s\n", msg)
}

// Skip prints a – skipped line.
func Skip(msg string) {
	fmt.Printf("  – %s\n", msg)
}

// Warn prints a warning line.
func Warn(msg string) {
	fmt.Fprintf(os.Stderr, "  ! %s\n", msg)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
