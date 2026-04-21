// Package query is the *only* package that reads from the index database.
// All transports (mcp, http, cli) call into this package so the agent-facing
// surface stays consistent across interfaces.
package query
