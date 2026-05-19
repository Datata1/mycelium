// Package main is the entry point for the sample fixture program.
// Exercised by integration tests; do not refactor without updating assertions.
package main

import (
	"fmt"
	"strings"
)

// Greeter produces localized greetings.
type Greeter struct {
	Locale string
}

// NewGreeter returns a Greeter for the given locale.
func NewGreeter(locale string) *Greeter {
	return &Greeter{Locale: strings.ToLower(locale)}
}

// Greet returns a greeting string for the given name.
func (g *Greeter) Greet(name string) string {
	switch g.Locale {
	case "de":
		return "Hallo, " + name
	case "es":
		return "Hola, " + name
	default:
		return "Hello, " + name
	}
}

func main() {
	g := NewGreeter("de")
	fmt.Println(g.Greet("World"))
}
