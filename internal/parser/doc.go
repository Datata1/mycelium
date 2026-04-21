// Package parser wraps tree-sitter and dispatches to per-language extractors.
// Parsers emit plain Symbol and Reference structs; they have no dependency on
// storage or any other package beyond the shared types defined here.
package parser
