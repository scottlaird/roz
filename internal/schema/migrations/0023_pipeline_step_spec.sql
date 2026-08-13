-- A per-step parameter, and somewhere to keep a wait whose target is not
-- known yet.
--
-- A pipeline step was a verb and nothing else, which is enough for a chain
-- whose steps all mean the same thing everywhere: undraft, merge. It is not
-- enough for a step that has to say *what* it waits for. Both `wait_ref` and,
-- eventually, a review step naming which group it waits for (scottlaird/roz#100)
-- need a verb plus a target, so this is one column rather than two features'
-- worth of columns.
--
-- Opaque here on purpose. What a spec means is the verb's business: the schema
-- knows a step may carry one, and nothing more. That is the same limit
-- actionverb draws around predicates -- names, never behaviour.
ALTER TABLE pipeline_step ADD COLUMN spec TEXT;

-- A ref wait whose version is not decided yet.
--
-- A release gate in a pipeline is written relative to wherever the repository
-- has got to: "block until two minors on from here". The instantiated action
-- cannot carry that as a wait, because a wait names a concrete constraint --
-- and the number it is relative to is a fact about the repository that has to
-- be read before it can be worked out.
--
-- Resolving at instantiation would mean reading GitHub while closing an
-- action, and nothing in that cascade does network I/O: closing is a database
-- operation and should not be able to fail because GitHub is slow. So a step
-- instantiates into this table instead, and the next sync -- which polls this
-- repository *because* of this row -- reads the tags, works out the bound,
-- writes the real wait and deletes this.
--
-- It resolves once and is then frozen, which is the property that matters. A
-- bound re-derived on every poll would move its own goalposts: each release
-- that shipped would push the target out by one and the gate would never open.
--
-- Two tables rather than a nullable column on action_ref_wait, because these
-- are two states rather than one state with a hole in it: "work out what to
-- wait for" and "wait for this". The transition is one-way, and a predicate
-- reading action_ref_wait cannot accidentally see a wait that means nothing
-- yet.
CREATE TABLE action_ref_pending (
  action_id  TEXT PRIMARY KEY REFERENCES action(id),
  repo_id    TEXT NOT NULL REFERENCES github_repo(id),
  kind       TEXT NOT NULL CHECK (kind IN ('branch','tag')),
  -- the relative expression, as written on the step: [prefix/]component+n
  spec       TEXT NOT NULL CHECK (spec <> ''),
  created_at TEXT NOT NULL
) STRICT;

-- Sync polls the repositories it has something to resolve for, as well as the
-- ones something is already waiting on.
CREATE INDEX action_ref_pending_repo ON action_ref_pending(repo_id, kind);
