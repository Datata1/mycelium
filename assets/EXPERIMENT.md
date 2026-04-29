# myco v3.1 Field Test — Session Evaluation

**Date:** 2026-04-29  
**Task:** Create a `run-external-ui-test.ts` script + GitHub Action to run UI tests against private cloud instances (modeled after the existing external integration test setup).  
**Telemetry summary:**

| tool             | calls | ok | in_total | out_total | p50  | p95  |
|------------------|-------|----|----------|-----------|------|------|
| ping             | 5     | 5  | 0 B      | 75 B      | 0s   | 0s   |
| search_lexical   | 4     | 4  | 311 B    | 16 B      | 1ms  | 17ms |
| list_files       | 3     | 3  | 103 B    | 4.4 KiB   | 1ms  | 1ms  |
| impact_analysis  | 2     | 1  | 151 B    | 168 B     | 36ms | 36ms |
| read_focused     | 2     | 0  | 73 B     | 0 B       | 0s   | 0s   |
| stats            | 1     | 1  | 0 B      | 2.8 KiB   | 40ms | 40ms |
| **all**          | **17**| **14** | **638 B** | **7.5 KiB** | **1ms** | **40ms** |

---

## What Worked Well

### 1. `list_files` for name-based discovery — strong signal
The agent used `list_files` with `name_contains` three times:
- `name_contains: "ui-test"` → immediately returned both `src/ci/ui-test.ts` and the `.d.ts` counterpart. Zero friction.
- `name_contains: ".github/workflows"` → returned nothing (YAML not indexed — see failures), so the agent moved on fast.
- `name_contains: "external"` → returned a broad set of results and the agent correctly picked `src/ci/run-external-integration-test.ts` out of them.

**Why it mattered:** In a monorepo with hundreds of packages, finding the canonical path of a file by partial name in 1ms is exactly the kind of lookup where myco beats `find`. The signal-to-noise was high.

### 2. `search_lexical` latency is excellent
All four `search_lexical` calls that returned data did so in ≤1ms (p50). When the index had coverage, the tool was effectively zero-cost for the agent.

### 3. Graceful fallback — the agent self-recovered
When myco tools returned `null` or errored, the agent immediately fell back to `Bash` or the `Read` tool without getting stuck. The workflow completed successfully. This says something good about the error surface — failures were clean rather than confusing.

### 4. `impact_analysis` shows correct tool instinct
The agent reached for `impact_analysis` (1 of 2 calls succeeded), which is the right tool for understanding which files import a given module. This is a non-trivial query that would otherwise require either `grep` across the whole tree or manual trace-through of imports — myco saves real time here when it works.

---

## What Didn't Work Well

### 1. `read_focused` — 0/2 success rate, the worst failure of the session

**What happened:**  
Both calls failed completely — 0 bytes out, 0s response time (immediate error):
- Call 1: `path: "src/ci/ui-test.ts"` → `"no such file or directory"` (daemon tried to open it as an absolute path from the wrong root)
- Call 2: `path: "packages/scripts/src/ci/ui-test.ts"` → `"file not in index"`

The agent had already seen `src/ci/ui-test.ts` returned by `list_files` — so the index knew about the file. But `read_focused` couldn't serve it with either path form.

**The root cause:** Path resolution inconsistency. `list_files` stores paths relative to the indexed project root (monorepo-4), but `read_focused` failed to resolve the same relative path when it tried to open the file on disk. The two tools weren't using the same base path.

**What would have been better:** If `read_focused` were fixed to use the same path the index stores (the `list_files` path), both calls would have succeeded. As a workaround, `get_file_outline` or `find_symbol` would have been more robust — they operate on indexed data only and don't need to re-open the file.

**Impact:** The agent had to fall back to a full `Read` of the 400-line `ui-test.ts`. That works but bypasses myco's semantic windowing entirely.

---

### 2. `search_lexical` returned `null` for most queries — false negatives destroy trust

**What happened:**
- `search_lexical { pattern: "ui.test|ui-test|uitest|playwright|e2e", path_contains: ".github/workflows" }` → `null`
- `search_lexical { pattern: "integration-test", path_contains: ".github/workflows" }` → `null`
- `search_lexical { pattern: "run-external-integration-test" }` → `null`
- `search_lexical { pattern: "UI_TEST_URL|CS_USERNAME|CS_PASSWORD|UI_TEST_ID", path_contains: "ui-tests" }` → `null`

**Why this happened:**
- YAML files (`.github/workflows/*.yml`) are not indexed. Completely invisible to myco.
- The `path_contains: "ui-tests"` filter matched the `packages/ui-tests` directory, but the env vars were in `playwright.config.ts` which apparently wasn't indexed at that path, or the pattern didn't match its content.
- `run-external-integration-test` didn't match lexically because the FTS index may tokenize on `-` boundaries.

**What would have been better:**
- **Index YAML files.** GitHub Actions workflows are exactly the kind of structured files an agent needs to understand CI infrastructure. The entire `.github/` tree being dark is a significant gap for this class of task.
- **Document what file types are and aren't indexed** — if the agent knew YAML wasn't indexed, it would skip myco and go straight to `find` without wasting a round-trip.
- **FTS tokenization should handle hyphenated patterns** — or `search_lexical` should accept raw substring match as a fallback when regex returns nothing.

---

### 3. Agent bypassed myco for the very first read

**What happened:** The agent read `integration-test.ts` directly with the `Read` tool — this was the first substantive action in the session, before any myco exploration. Then it used myco for subsequent discovery.

**Why this matters:** The user explicitly asked to use myco for exploring the codebase. The file was available via myco, but the agent reached for `Read` first because the file path was already in the IDE context (the user had it open). This is understandable but means the user's instruction wasn't fully honored.

**What would have been better:** `get_file_outline` on `integration-test.ts` would have given the function signatures and imports in a fraction of the tokens, letting the agent build its mental model faster. Only use full `Read` if the outline wasn't sufficient.

---

### 4. `impact_analysis` had a 50% failure rate (1/2 ok)

One of the two `impact_analysis` calls failed (168B out vs 151B in suggests an error response). The session didn't expose what the failed call was for, but a 50% failure rate on a tool that should be doing indexed graph traversal is concerning.

**What would have been better:** If `impact_analysis` fails, it should return a structured error with the reason (node not found, depth exceeded, etc.) rather than a silent failure. The agent appeared to continue without handling it, which means it may have missed a relevant code path.

---

### 5. `get_file_outline`, `find_symbol`, and `search_semantic` were never used — missed opportunities

The agent never called:
- `get_file_outline` — would have let it scan the structure of the large `ui-test.ts` and `integration-test.ts` without reading every line
- `find_symbol` — would have let it locate `withCodesphereTestingDeployment`, `shouldUiTest`, etc. by name rather than reading whole files to find them
- `search_semantic` — would have been useful for "scripts that set up test environments against external deployments" — a conceptual query that lexical search can't handle

**Why this matters:** The agent's exploration pattern was essentially: try myco (often fail) → fall back to direct file read. If it had used `get_file_outline` and `find_symbol`, it could have mapped the codebase with far fewer tokens and without hitting the path-resolution bugs of `read_focused`.

---

## Summary

| Area | Grade | Notes |
|------|-------|-------|
| `list_files` for discovery | ✅ Good | Worked as intended, fast, reliable |
| `search_lexical` speed | ✅ Good | Sub-ms when the index had coverage |
| `read_focused` reliability | ❌ Bad | 0/2 — path resolution broken |
| YAML/workflow indexing | ❌ Missing | Entire `.github/` tree is dark |
| Tool breadth used by agent | ⚠️ Partial | `get_file_outline`, `find_symbol`, `search_semantic` unused |
| Error clarity | ⚠️ Partial | Silent `null` returns from lexical search; no fallback hint |
| Net value vs. no myco | ⚠️ Marginal | `list_files` saved some `find` calls; everything else required fallback |

**Bottom line:** myco added real value exactly once — `list_files` found the right files in a deep monorepo instantly. Everything else either failed silently (`search_lexical` null returns), broke entirely (`read_focused`), or was never tried (outline/symbol/semantic tools). For this session, the agent would have reached the same outcome faster by leaning on `Bash` + `Read` directly — the myco overhead (failed calls + ToolSearch loading) cost more than it returned.

The single highest-leverage fix: **make `read_focused` use the same path resolution as `list_files`**. If that call had worked, the agent's exploration pattern would have been substantially more efficient.
