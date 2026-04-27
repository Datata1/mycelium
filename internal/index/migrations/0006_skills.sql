-- v2.5: incremental skills regeneration.
--
-- Tracks the hash of every rendered file in the skills tree
-- (packages/<dir>/SKILL.md, aspects/<name>/INDEX.md, the root
-- INDEX.md) so the daemon can short-circuit unchanged files and
-- avoid rewriting them — which would bump modtimes, churn watcher
-- pipelines in adjacent worktrees, and dirty git status in the rare
-- repos that commit the tree.
--
-- The hash input is the rendered byte stream; we hash output rather
-- than input so cross-package ref-count drift (a downstream package
-- starts/stops calling this one) naturally invalidates the cache
-- without needing a separate dependency graph.
--
-- Path is the tree-relative output path with forward slashes
-- ("packages/internal/query/SKILL.md", "aspects/error-handling/
-- INDEX.md", "INDEX.md"). No FK to anything: skill files are derived
-- from the index, not stored alongside it. Orphan rows (a package
-- was deleted) are pruned explicitly during regen.

CREATE TABLE skill_files (
    path         TEXT    PRIMARY KEY,
    skill_hash   TEXT    NOT NULL,
    generated_at INTEGER NOT NULL
);
