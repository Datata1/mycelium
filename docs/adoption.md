# Adoption: is your agent actually using mycelium?

Mycelium ships a daemon, MCP tools, an HTTP API, a skills tree, and
focused reads. None of that helps if the agent in front of you still
reaches for `Bash(grep)` and `Read` on every turn. This page is a
practical checklist for verifying that your agent is reaching for
mycelium and a guide to interpreting the telemetry log when it isn't.

## TL;DR

1. Turn telemetry on (`telemetry.enabled: true` in `.mycelium.yml`).
2. Run a normal coding session for a day.
3. `myco stats --telemetry` — the rows tell you which tools the agent
   actually called and what fraction of the read budget they replaced.
4. If `find_symbol`, `get_neighborhood`, `read_focused`, and the
   skills-tree paths are largely missing from the log, the agent isn't
   using mycelium yet — see [Common shapes of "not using
   mycelium"](#common-shapes-of-not-using-mycelium) below.

## The five-minute setup check

Before anything else: make sure mycelium is reachable from the agent at
all. A surprising fraction of "the agent isn't using mycelium" cases
turn out to be MCP wiring problems.

- [ ] `myco daemon` is running. `pgrep -af 'myco daemon'` shows a
      live process; `.mycelium/daemon.sock` exists.
- [ ] `myco doctor` is green. Self-loop count, unresolved-ref ratio,
      and `interface_expansion_coverage` are all in the pass band.
- [ ] The MCP server is registered with the agent. For Claude Code,
      `myco init --mcp claude` prints the JSON snippet for
      `~/.claude.json`. After editing the file, **restart the agent**:
      MCP servers load at startup, not per-session.
- [ ] `myco stats` returns non-zero counts for files / symbols / refs.
      A zero-state index will silently no-op every query and look
      indistinguishable from "agent didn't try."

## Turning telemetry on

Telemetry is **off by default** and **local-only** — it never leaves
the host. Open `.mycelium.yml` (or create it at the repo root) and
add:

```yaml
telemetry:
  enabled: true
  # path: .mycelium/telemetry.jsonl   # default; override if you want
                                       # the log somewhere else
```

Restart the daemon (`pkill -f 'myco daemon' && myco daemon &`). New
log lines appear in `.mycelium/telemetry.jsonl` from the next IPC
call onward. The format is one JSON object per line:

```json
{"ts":"2026-04-27T10:14:33Z","tool":"find_symbol","in_bytes":42,"out_bytes":1180,"dur_ms":8,"ok":true}
```

Stable fields: `ts`, `tool`, `in_bytes`, `out_bytes`, `dur_ms`, `ok`.
You can `tail -f` the file during a session to watch what the agent
calls in real time.

## Reading `myco stats --telemetry`

After enough activity to be representative (a day or two of normal
work; ten or more sessions), summarize:

```text
$ myco stats --telemetry
tool                   count    ok    in_bytes   out_bytes   p50    p95
find_symbol               87    87       3.6KB      94.2KB    6ms   18ms
get_neighborhood          54    54       2.1KB     217.8KB   12ms   41ms
get_file_outline          41    41       1.7KB      63.4KB    4ms    9ms
read_focused              19    19       0.8KB      72.6KB    7ms   22ms
search_lexical            12    12       0.4KB      14.0KB   31ms   88ms
stats                      6     6        12B       11.8KB    2ms    4ms
all                      219   219       8.6KB     473.8KB    7ms   34ms
```

What to look for:

- **Tool diversity.** If the only rows are `stats` and `ping`, the
  agent never queried for code. Either it doesn't know mycelium is
  available, or its instructions push it elsewhere. Check the MCP
  registration first.
- **`find_symbol` and `get_references` near the top.** These are the
  bread-and-butter queries that replace `grep -rn 'symbolName'`. If
  they're absent but `search_lexical` is heavy, the agent is using
  mycelium as a smarter `grep` and missing the structural tools. A
  hint about reaching for `find_symbol` first usually fixes this.
- **`get_neighborhood` / `impact_analysis` / `critical_path`** show
  graph traversal — the structurally distinctive thing mycelium
  offers. Their presence means the agent is treating the codebase as
  a graph, not a string corpus.
- **`read_focused`** with `in_bytes` ≪ `out_bytes` is the Pillar I
  signal: the agent is asking for narrow files instead of full
  reads.
- **p95 durations** above ~200 ms on `get_neighborhood` are an early
  warning that you're hitting the SQLite-graph ceiling that gates the
  v4 graph-DB rewrite. Worth filing if you see it.

The `all` row gives you a single number for total agent-mycelium
traffic across a window.

## Comparing mycelium reads to raw reads

Telemetry only sees mycelium calls. To know whether the agent is
*also* doing raw `Read` / `Bash(grep)`, you have to look at the agent
side. Strategies in rough order of how close they get to the truth:

1. **The session transcript.** Most agents save per-session logs
   somewhere. Grep for `Read(`, `Bash(`, and your MCP tool names; the
   ratio between mycelium-tool calls and raw reads is the signal.
2. **Watch the working session.** Open the telemetry log in one
   terminal (`tail -f .mycelium/telemetry.jsonl`) and the agent in
   another. When the agent claims to have "looked up the function,"
   a `find_symbol` line should appear within a second.
3. **The skills tree.** If the agent uses the `.mycelium/skills/`
   filesystem, those reads show up as `Read` calls in the transcript
   — they don't go through MCP, so they're invisible in the
   telemetry log. That's by design (the skills tree is meant to
   route around MCP), but it means you have to look in two places to
   see the full picture.

## Common shapes of "not using mycelium"

Patterns we see in the telemetry log when adoption isn't happening,
ordered most to least frequent.

### "Empty log"

Hours of agent activity, telemetry log is empty or only has `ping`.
The agent isn't talking to mycelium at all.

- Check MCP registration (`myco init --mcp claude` snippet present
  in the agent's config?).
- Restart the agent — MCP servers load at startup.
- Try a hand-written prompt: "Use the mycelium MCP tools to find
  references to FooBar." If that produces traffic, the issue is
  prompt-priming; otherwise it's plumbing.

### "search_lexical only"

Hundreds of `search_lexical` calls, no `find_symbol` /
`get_neighborhood` / `get_references`. The agent treats mycelium as
a faster `grep` and misses the graph layer.

- This is usually a tools-prompt issue. The agent doesn't know which
  mycelium tool to reach for and falls back to the one that looks
  most like its training-data reflex.
- Add a one-line hint in the agent's instructions: "When asking
  about code, prefer `find_symbol` for definitions and
  `get_references` for callers; use `search_lexical` only for
  literal strings."

### "Stats and nothing else"

The agent calls `stats` once at the start of every session and
nothing afterwards. It's confirming mycelium is alive but not
querying it.

- Often happens when the agent has been told to "verify mycelium is
  reachable" but not given a clear signal of when to use it.
- Same fix as above — give it a use-case-keyed instruction, not a
  ceremonial one.

### "Big find_symbol fan-out, no read_focused"

The agent finds the symbol, then `Read`s the whole file (visible only
in transcripts, not telemetry). Token usage stays high.

- The agent has v2.4's `read_focused` available but isn't using it.
- Hint: "After locating a symbol, prefer `read_focused` with the
  question as the focus over reading the full file."

### "find_symbol bursts followed by silence"

Spikes of mycelium traffic for a few minutes at the start of a task,
then nothing for the rest. The agent is mapping the area once and
working from memory.

- This is actually fine for short tasks. For longer ones, look for
  whether the agent is using stale information by the end. Hard to
  spot purely from telemetry — look at the transcripts.

## What "good" looks like

A representative healthy log over a workday of agent activity:

- 50–200 total mycelium calls, spread across 4–7 distinct tools.
- `find_symbol` and `get_references` together account for 30–60% of
  calls; `get_neighborhood` adds another 10–25%.
- `search_lexical` is 5–15% — present, but not the dominant tool.
- `read_focused` shows up regularly for files larger than a few KB.
- Skills-tree reads (in the agent's transcript, not the telemetry
  log) appear at the start of unfamiliar work — `INDEX.md`,
  one or two per-package `SKILL.md`s, occasional aspect reads.
- `myco doctor` is green and the index ages by minutes, not hours.

If you see something different and it's working, write it up. The
v3 release notes will collect adoption-pattern reports as a way to
calibrate this page against real usage.

## When telemetry says nothing's wrong but it still feels off

Telemetry can't tell you whether the agent's *answers* improved when
it started using mycelium. For that, the right comparison is your own
sense of work quality across two sessions, one with mycelium reachable
and one without. The CLAUDE.md guidance — start agent sessions with
"prefer mycelium for code lookup" — is a soft prompt; a harder
A/B is to disable the daemon for a session and see whether the agent
notices.

If you find that the agent *should* be using mycelium and isn't, and
your telemetry log + transcripts agree but you can't fix it with
prompt changes, that's exactly the data we want to hear about. File
an issue with a redacted snippet of the log and a one-line summary
of what the agent did instead.
