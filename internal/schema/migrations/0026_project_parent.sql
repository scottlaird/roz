-- A project's parent, so a list of forty can be read as the handful of things
-- it actually is.
--
-- Display only, deliberately. A parent does not block a child, closing a
-- parent does not close its children, and nothing about priority or ranking
-- reads this. Those are relationships roz already has -- project_blocks says
-- one must finish before another -- and conflating "is part of" with "waits
-- for" would make both mean less. What this adds is a way to read a long list.
--
-- Nullable, and most projects have none: a hierarchy is worth having where
-- there is one, and a flat list is the honest shape of everything else.
--
-- The CHECK stops the shortest cycle. Longer ones cannot be expressed in a
-- constraint over one row, so the command walks the ancestors before writing
-- -- but a project that is its own parent is worth refusing here too, since
-- that is the one a mistyped identifier produces.
ALTER TABLE project ADD COLUMN parent_id TEXT
  REFERENCES project(id)
  CHECK (parent_id IS NULL OR parent_id <> id);

-- Reading a tree walks from each project to its children, so this is the
-- direction the index has to serve.
CREATE INDEX project_parent ON project(parent_id) WHERE parent_id IS NOT NULL;
