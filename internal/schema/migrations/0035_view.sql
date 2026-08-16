-- A listing you can name and come back to.
--
-- Everything a view holds already exists as a flag: which listing, which
-- columns, which order, which filter. What it did not have was a name, so
-- every question worth asking twice had to be retyped, and the interesting
-- ones are long -- "live projects with no open action and nothing snoozing
-- them" is a line of CEL nobody wants to write from memory on a Monday.
--
-- The point is that adding one needs no code. A filter is an expression over
-- the columns a listing already declares, so a new question is a row in this
-- table rather than a flag, a query and a release. That is what makes this
-- worth being an entity: the alternative is a --saved-view flag whose values
-- are compiled in.
--
-- Named rather than numbered. Every other entity here gets an allocated
-- identifier because it is one of many similar things and the number is how
-- you refer to it; a view is referred to by what it is for. `roz project list
-- --view stalled` reads as a sentence, and SL7 would not.
CREATE TABLE view (
  -- what you type. Lower case, no spaces: it is an argument, and a name
  -- needing quotes is a name nobody uses.
  name       TEXT PRIMARY KEY CHECK (
               name <> '' AND name = lower(name) AND instr(name, ' ') = 0),

  -- which listing it belongs to: 'project', 'action', 'pr', 'issue' and so on.
  -- Not constrained to a list here, because the listings are a fact about the
  -- command tree rather than about the schema, and a CHECK naming them would
  -- be a second place to edit when one is added. The command validates it
  -- against the listings that actually exist, which is the check that can be
  -- kept true.
  entity     TEXT NOT NULL CHECK (entity <> ''),

  -- the three flags, stored as typed. Empty means "not part of this view",
  -- which is different from "empty result" or "no columns": a view that says
  -- nothing about sorting leaves the listing's own default alone.
  filter     TEXT NOT NULL DEFAULT '',
  sort       TEXT NOT NULL DEFAULT '',
  fields     TEXT NOT NULL DEFAULT '',

  -- what the view is for, in a sentence. Prose, because the name is short by
  -- design and the filter is not always legible -- priority 1 is the *highest*
  -- priority, so "urgent" reads as `priority <= 2`, which is backwards to
  -- anybody who has not been told.
  description TEXT NOT NULL DEFAULT '',

  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

-- the listing's own question: which views can it offer
CREATE INDEX view_entity ON view(entity);
