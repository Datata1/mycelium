// Package skills generates the on-disk Markdown tree mycelium emits to
// .mycelium/skills/. The tree is the v3 "agent-native" pillar: a
// browseable filesystem of structural facts an agent can navigate with
// only the Read tool, complementing (not replacing) MCP for programmatic
// queries.
//
// v2.3 ships the deterministic generator + `myco skills compile` CLI.
// Output is a snapshot regenerated from scratch on every command run;
// incremental hash-gated regeneration is v2.5.
//
// Architectural rules this package respects:
//
//   - Reads only via internal/query.Reader; no raw SQL here.
//   - Writes only the .mycelium/skills/ subtree; never touches index.db.
//   - All output is byte-deterministic given a fixed Reader state, so
//     golden file tests are meaningful and incremental hashing (v2.5)
//     can use a stable hash.
package skills
