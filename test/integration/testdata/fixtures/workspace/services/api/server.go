package main

// APIHandler serves the HTTP endpoints of the api project.
type APIHandler struct{}

// Serve starts the HTTP server.
func (h *APIHandler) Serve() error { return nil }

// APIOnlySymbol is unique to the api project so workspace scoping tests
// can assert the project filter works.
func APIOnlySymbol() string { return "api" }
