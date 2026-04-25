# Research informing mycelium

This document records the published research that has shaped mycelium's
design. Each entry names a paper, briefly summarizes its findings, and
states what specifically in mycelium it informed. We add to this list as
new research informs new pillars.

The bar for inclusion: the paper changed a concrete decision about how
mycelium is built. Generally-relevant LLM/RAG papers we read but didn't
act on are not listed here.

We separate "informed the design" from "we reproduced this." Mycelium is
not a reproduction of any of these papers — they shaped which problems
we treat as worth solving and how, but our implementations differ in
specifics. See per-entry notes for details.

## Bibliography

### Chinthareddy, M. R. (2026)

**Reliable Graph-RAG for Codebases: AST-Derived Graphs vs LLM-Extracted
Knowledge Graphs.** arXiv:2601.08773.

Benchmarks three retrieval pipelines (vector-only, LLM-extracted graph,
deterministic AST-derived graph) on three Java codebases (Shopizer,
ThingsBoard, OpenMRS Core) using a fixed 15-question suite per
repository. Across repositories the deterministic AST graph achieves
the highest correctness (15/15 on Shopizer vs 13/15 LLM-KB and 6/15
vector-only), the lowest indexing latency (seconds vs minutes for
LLM-KB), and the lowest end-to-end cost (~2× the vector baseline vs
~20-45× for LLM-KB). LLM-mediated extraction also exhibits probabilistic
file-skipping (31% on Shopizer) that creates retrieval blind spots.

**Informed in mycelium:**

- Provides external empirical validation for our load-bearing
  architectural rules: "no LLM at indexing time" and "AST + typed
  refs over LLM-extracted dependency graphs" (CLAUDE.md).
- The paper's "interface-consumer expansion" mechanic (Algorithm 1,
  Listing 2, the `InterfaceConsumerExpand` step) is the basis for v2.1
  Pillar J: when querying neighborhoods on a concrete type, the query
  layer fans out to interfaces it implements (and vice versa) via
  `RefInherit` edges. Implementation lives in
  `internal/resolver/golang/resolver.go` (edge emission via
  `computeImplementsGraph` + `EmitInheritance`) and
  `internal/query/neighborhood.go` (query-time expansion via
  `expandSeedViaInheritance`).
- The cost comparison (45× LLM-KB cost on multi-repo workloads) and
  reliability finding (31% file skip rate) are cited in the README's
  "Why deterministic AST graphs?" section.

### Sun, Y., Wei, P., & Hsieh, L. B. (2026)

**Don't Retrieve, Navigate: Distilling Enterprise Knowledge into
Navigable Agent Skills for QA and RAG.** arXiv:2604.14572. Magellan
Technology Research Institute.

Distills a document corpus into a hierarchical skill directory offline
and lets an LLM agent navigate it at serve time using its native
filesystem tools. Replaces embedding-based retrieval with progressive
disclosure: skill metadata is auto-loaded, full SKILL.md and INDEX.md
files are read on demand, leaf documents are fetched via a single
`get_document(id)` tool. On WixQA, this approach beats dense retrieval
(F1 0.460 vs 0.363, +27%), RAPTOR, and agentic-RAG on every quality
metric.

**Informed in mycelium:**

- Drives v3 Pillar H ("compile-to-skills"): mycelium will materialize
  a deterministic, browsable filesystem at `.mycelium/skills/` so
  agents can navigate the code graph using `Read` / `Glob` — tools
  they already trust — rather than learning new MCP schemas. We adopt
  the progressive disclosure pattern (metadata → SKILL.md → INDEX.md
  → leaf), but our hierarchy follows authoritative module/package
  boundaries rather than K-means embedding clusters, since code already
  has structure we don't need to infer.
- We deliberately do not adopt the paper's strong-form thesis ("LLM
  only at serve time, no retrieval infra"); MCP tools stay as a
  complementary surface for programmatic queries.

### Wang, Y., Shi, Y., Yang, M., et al. (2026)

**SWE-Pruner: Self-Adaptive Context Pruning for Coding Agents.**
arXiv:2601.16746. Shanghai Jiao Tong University LLMSE Lab + Douyin.

Empirically observes that coding agents on SWE-Bench spend 76% of their
token budget on read operations. Introduces a focus-aware middleware
that augments file-reading tools with an optional natural-language
goal-hint parameter; a 0.6B neural skimmer scores lines against the
hint and returns only the relevant ones (line numbers preserved). On
SWE-Bench Verified, this drops token usage 23-38% **and improves
success rate** by 1.2-1.4 percentage points. Line-level granularity
preserves AST validity (87% AST-correct vs 0.29% for token-level
LLMLingua2).

**Informed in mycelium:**

- Drives v3 Pillar I ("focused reads"): mycelium tools will accept an
  optional `focus` parameter, and a new `myco read` primitive will
  return files with relevant symbols expanded and others collapsed to
  one-line signatures. We adopt the *pattern* (focus-aware filtering,
  line-level granularity, backward-compatible parameter augmentation)
  but not the *mechanism* — a 0.6B neural skimmer would break our
  one-static-binary distribution story and our "no LLM at query time"
  rule. Our filter is deterministic: symbol-name match, docstring
  keyword match, and one-hop graph expansion.
- The 76% read-overhead finding motivates v3 Pillar K (adoption
  telemetry): before scoping focused reads, we want to verify the
  problem on real mycelium workloads — our existing tools like
  `get_file_outline` may already keep per-call output small enough
  that the gap is smaller than SWE-Bench's raw-`cat` baseline.

## On honest attribution

The papers above are independent academic work, not endorsements of
mycelium. They examined related problems in their own settings (Java
graph-RAG benchmarks, enterprise document QA, agent context pruning on
SWE-Bench) and reached conclusions that, taken together, suggest
mycelium's architectural bets are pointing in defensible directions.
We cite them because they changed our code, not because they validate
us in the abstract.

When a future entry is added that contradicts a current decision in
mycelium, we change the code or document why we're holding the line.
