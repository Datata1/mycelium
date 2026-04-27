# Research informing mycelium

This document records the published research that has shaped mycelium's
design. Each entry names a paper, briefly summarizes its findings, and
states what specifically in mycelium it informed. We add to this list
as new research informs new pillars.

The bar for inclusion: the paper changed a concrete decision about how
mycelium is built. Generally-relevant LLM/RAG papers we read but
didn't act on are listed at the end under [Read but not acted
on](#read-but-not-acted-on) — they shape our priors but not the code.

We separate "informed the design" from "we reproduced this." Mycelium
is not a reproduction of any of these papers — they shaped which
problems we treat as worth solving and how, but our implementations
differ in specifics. See per-entry notes for details.

## How to use this document

If you're trying to understand a non-obvious decision in the codebase
— "why is there an interface-consumer expansion step in the query
layer?", "why does the focused-reads filter use lexical scoring
instead of a neural reranker?", "why does the project ship a skills
tree on top of a perfectly good MCP API?" — the answer is usually in
one of the three core entries below. The
[Design-decision crosswalk](#design-decision-crosswalk) section maps
specific code locations to the papers that motivated them, in both
directions.

If you're an outside reader trying to evaluate mycelium's claims, the
[On honest attribution](#on-honest-attribution) section spells out
exactly which findings we're citing as inspiration vs. which we
ourselves reproduced.

## Bibliography

### Chinthareddy, M. R. (2026)

**Reliable Graph-RAG for Codebases: AST-Derived Graphs vs LLM-
Extracted Knowledge Graphs.** arXiv:2601.08773.

Benchmarks three retrieval pipelines (vector-only, LLM-extracted
graph, deterministic AST-derived graph) on three Java codebases
(Shopizer, ThingsBoard, OpenMRS Core) using a fixed 15-question
suite per repository. Across repositories the deterministic AST graph
achieves the highest correctness (15/15 on Shopizer vs 13/15 LLM-KB
and 6/15 vector-only), the lowest indexing latency (seconds vs
minutes for LLM-KB), and the lowest end-to-end cost (~2× the vector
baseline vs ~20-45× for LLM-KB). LLM-mediated extraction also
exhibits probabilistic file-skipping (31% on Shopizer) that creates
retrieval blind spots.

**Informed in mycelium:**

- Provides external empirical validation for our load-bearing
  architectural rules: "no LLM at indexing time" and "AST + typed
  refs over LLM-extracted dependency graphs" (CLAUDE.md).
- The paper's "interface-consumer expansion" mechanic (Algorithm 1,
  Listing 2, the `InterfaceConsumerExpand` step) is the basis for
  v2.1 Pillar J: when querying neighborhoods on a concrete type, the
  query layer fans out to interfaces it implements (and vice versa)
  via `RefInherit` edges. Implementation lives in
  `internal/resolver/golang/resolver.go` (edge emission via
  `computeImplementsGraph` + `EmitInheritance`) and
  `internal/query/neighborhood.go` (query-time expansion via
  `expandSeedViaInheritance`).
- The cost comparison (45× LLM-KB cost on multi-repo workloads) and
  reliability finding (31% file skip rate) are cited in the README's
  "Why deterministic AST graphs?" section.

**What we did not adopt:** the paper's specific ranking weights for
interface-consumer fan-out. Mycelium currently treats interface
consumers as equal-weight neighbors. If we ever surface a
"likeliness-of-impact" score on `impact_analysis`, the paper's
weights are the place to start.

### Sun, Y., Wei, P., & Hsieh, L. B. (2026)

**Don't Retrieve, Navigate: Distilling Enterprise Knowledge into
Navigable Agent Skills for QA and RAG.** arXiv:2604.14572. Magellan
Technology Research Institute.

Distills a document corpus into a hierarchical skill directory
offline and lets an LLM agent navigate it at serve time using its
native filesystem tools. Replaces embedding-based retrieval with
progressive disclosure: skill metadata is auto-loaded, full SKILL.md
and INDEX.md files are read on demand, leaf documents are fetched
via a single `get_document(id)` tool. On WixQA, this approach beats
dense retrieval (F1 0.460 vs 0.363, +27%), RAPTOR, and agentic-RAG
on every quality metric.

**Informed in mycelium:**

- Drives v3 Pillar H ("compile-to-skills"): mycelium materializes a
  deterministic, browseable filesystem at `.mycelium/skills/` so
  agents can navigate the code graph using `Read` / `Glob` — tools
  they already trust — rather than learning new MCP schemas. We
  adopt the progressive disclosure pattern (root `INDEX.md` →
  per-package `SKILL.md` → `aspects/<name>/INDEX.md`), but our
  hierarchy follows authoritative module/package boundaries rather
  than K-means embedding clusters, since code already has structure
  we don't need to infer.
- The paper's "small SKILL.md, drill down on demand" recommendation
  shapes the v2.3 layout: per-package files are kept under ~160
  lines on the largest mycelium package, with structured links
  back to MCP tools (`myco query refs <symbol>`) for the deep dive.

**What we did not adopt:** the paper's strong-form thesis ("LLM
only at serve time, no retrieval infra"). MCP tools stay as a
complementary surface for programmatic queries — see the
[Design-decision crosswalk](#design-decision-crosswalk) below for
why "complementary" beat "replace MCP" on our list.

### Wang, Y., Shi, Y., Yang, M., et al. (2026)

**SWE-Pruner: Self-Adaptive Context Pruning for Coding Agents.**
arXiv:2601.16746. Shanghai Jiao Tong University LLMSE Lab + Douyin.

Empirically observes that coding agents on SWE-Bench spend 76% of
their token budget on read operations. Introduces a focus-aware
middleware that augments file-reading tools with an optional
natural-language goal-hint parameter; a 0.6B neural skimmer scores
lines against the hint and returns only the relevant ones (line
numbers preserved). On SWE-Bench Verified, this drops token usage
23-38% **and improves success rate** by 1.2-1.4 percentage points.
Line-level granularity preserves AST validity (87% AST-correct vs
0.29% for token-level LLMLingua2).

**Informed in mycelium:**

- Drives v3 Pillar I ("focused reads", v2.4): mycelium tools accept
  an optional `focus` parameter, and the `read_focused` MCP tool /
  `myco read --focus` CLI returns files with relevant symbols
  expanded and others collapsed to one-line signatures. We adopt the
  *pattern* (focus-aware filtering, line-level granularity,
  backward-compatible parameter augmentation) but not the
  *mechanism* — a 0.6B neural skimmer would break our one-static-
  binary distribution story and our "no LLM at query time" rule.
  Our filter is deterministic: symbol-name match (3.0 exact / 2.0
  substring), qualified-name match (2.0), docstring (1.0),
  ref-target (0.5).
- The 76% read-overhead finding motivates v3 Pillar K (adoption
  telemetry, v2.2): before scoping focused reads, we wanted to
  verify the problem on real mycelium workloads — our existing tools
  like `get_file_outline` may already keep per-call output small
  enough that the gap is smaller than SWE-Bench's raw-`cat`
  baseline. The opt-in `.mycelium/telemetry.jsonl` log lets users
  measure this on their own workloads.

**What we did not adopt:** SWE-Pruner's reranker training pipeline
or the 0.6B model itself. Mycelium's distribution is one statically-
linked binary plus an optional sqlite-vec extension; bundling a
600M-parameter model would multiply binary size by roughly 600× and
introduce GPU/CPU performance variance into something users today
think of as a CLI. The honest cost of skipping the neural mechanism
is precision: our v2.4 self-index measurements show 29-81% byte
reduction depending on focus specificity, vs. SWE-Pruner's 23-54%
range against a trained reranker. We trade some precision for
distribution simplicity.

## Design-decision crosswalk

Forward direction (paper → code), for readers asking "where does
this idea live?":

| Paper | Mycelium location | What's there |
|---|---|---|
| Chinthareddy 2026 §6 (interface-consumer expansion) | `internal/resolver/golang/resolver.go` | Emits `RefInherit` edges (concrete → interface) at index time. |
| Chinthareddy 2026 §6 | `internal/query/neighborhood.go` (`expandSeedViaInheritance`) | Walks `RefInherit` edges at query time so neighbourhood/impact/critical-path traversals fan out to interface consumers. |
| Chinthareddy 2026 §4 (cost/correctness benchmark) | `README.md` "Why deterministic AST graphs?" | Cites the 15/15 vs 6/15 result and the 45× cost gap. |
| Sun et al. 2026 §3 (progressive-disclosure layout) | `internal/skills/compile.go` | Renders `INDEX.md` → `packages/<dir>/SKILL.md` → `aspects/<name>/INDEX.md` as a navigable tree under `.mycelium/skills/`. |
| Sun et al. 2026 §3 (frontmatter-driven metadata) | `internal/skills/compile.go` (`renderPackageSkill`) | Each SKILL.md begins with a YAML frontmatter block (name, description, level, language, files, symbols) so an agent can decide whether to read deeper without consuming the body. |
| Wang et al. 2026 §3 (focus-aware middleware) | `internal/focus/` + `internal/query/read.go` | Lexical focus filter; `ReadFocused` collapses non-matching symbols to one-line markers in the file's native comment style. |
| Wang et al. 2026 §3 (backward-compatible parameter) | `internal/ipc/proto.go` | `Focus` field added to `FindSymbolParams` / `GetFileOutlineParams` / `GetNeighborhoodParams`; empty value preserves prior behaviour. |
| Wang et al. 2026 §2 (76% read-budget motivation) | `internal/telemetry/` + `cmd/myco stats --telemetry` | Local-only JSONL log of every IPC call; aggregator surfaces per-tool counts and byte totals so users can see whether mycelium tools are actually replacing raw reads. |

Reverse direction (decision → paper), for readers asking "why is
the code this way?":

- **No LLM at indexing time.** Chinthareddy 2026 — LLM extraction
  is slower, more expensive, and probabilistically skipped 31% of
  Shopizer's files.
- **Interface-consumer expansion in `get_neighborhood` / impact.**
  Chinthareddy 2026 §6 — direct-edge traversal misses callers who
  depend on the interface, not the concrete impl.
- **Skills tree on the filesystem (`.mycelium/skills/`).** Sun et
  al. 2026 — agents reason better about data they see in
  filesystem layout than data behind an opaque retrieval API.
- **Skills tree complements MCP rather than replacing it.** Our
  decision, against Sun et al. 2026's strong-form thesis. MCP is
  the better fit for programmatic / scripted use; the filesystem
  is the better fit for free-form agent navigation.
- **Focus parameter on existing tools is optional + back-compat.**
  Wang et al. 2026 §3 — they explicitly designed augmentation that
  doesn't break tool callers who don't opt in.
- **Lexical (not neural) focus filter.** Adapted from Wang et al.
  2026 — we keep the *pattern* but not the *mechanism* to preserve
  the one-static-binary distribution story.
- **Opt-in telemetry, default off.** Wang et al. 2026 §2 motivates
  the metric (read-budget consumption); the opt-in default is our
  privacy choice, not theirs.

## Read but not acted on

Papers we found relevant to mycelium's design space but that haven't
(yet) changed the code. Listed for transparency — if you wonder
"did they read X?", this is where to check.

- **LLMLingua / LLMLingua-2.** Token-level prompt compression. We
  rejected it for the focused-reads pillar because the token-level
  granularity destroys AST validity (Wang et al. 2026 measured 0.29%
  AST-correct outputs at high compression). Mycelium's
  `read_focused` works at the symbol level for the same reason.
- **CodeBERT / GraphCodeBERT family.** Embedding-only approaches
  to code understanding. We use embeddings for `search_semantic`
  but treat them as a complement to the symbol/ref graph, not a
  replacement — Chinthareddy 2026's vector-only baseline at 6/15
  is the empirical reason.
- **HNSW and modern ANN indexes (DiskANN, ScaNN).** The
  `search_semantic` performance ceiling is currently sqlite-vec's
  flat-scan path at v0.1.9. HNSW lands when sqlite-vec ships it
  upstream; we do not plan a separate ANN integration in the
  meantime — it would multiply the deploy story by another moving
  part.
- **GraphRAG (Microsoft).** LLM-mediated entity extraction over
  documents. Excellent for prose corpora; the wrong shape for
  code, where the AST already gives us authoritative entities.
  Chinthareddy 2026 is the explicit comparison.
- **LSP integration as a freshness mechanism.** Tempting because
  every editor already runs an LSP. Rejected because it makes index
  freshness depend on editor state — close VSCode, mycelium goes
  stale. The watchman / fsnotify path keeps freshness independent
  of any client.

## On honest attribution

The papers above are independent academic work, not endorsements
of mycelium. They examined related problems in their own settings
(Java graph-RAG benchmarks, enterprise document QA, agent context
pruning on SWE-Bench) and reached conclusions that, taken together,
suggest mycelium's architectural bets are pointing in defensible
directions. We cite them because they changed our code, not because
they validate us in the abstract.

The only finding we have ourselves reproduced is the
sqlite-vec-vs-brute-force benchmark, which lives in
`internal/query/semantic_bench_test.go` and the README. The
correctness numbers (15/15 vs 6/15), cost ratios (45×), and
adoption results (+27% F1, 23-38% token reduction) all come from
the original papers; we have not run those experiments ourselves
and do not claim mycelium would land on identical numbers under
their experimental conditions.

When a future entry is added that contradicts a current decision in
mycelium, we change the code or document why we're holding the line.
