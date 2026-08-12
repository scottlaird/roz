-- Authored prose the page can place, keyed rather than attached to an item.
--
-- Some of what belongs on a queue page is text nothing can derive: an
-- introduction, what you have decided matters this week, a standing caveat. It
-- is authored, it is about no particular action or project, and it had nowhere
-- to live.
--
-- Distinct from `roz note`, which writes a comment into the log against a
-- subject precisely so that narrative stays out of the record. This is the
-- opposite case: content that exists in order to be displayed.
--
-- The key is a slot on the page, from a closed set the renderer knows. That is
-- deliberate on two counts. A note under a key nothing renders would be
-- invisible rather than wrong, with no error to say so; and a keyed blob store
-- with arbitrary keys is exactly how a page whose value is that almost all of
-- it is derived stops being that, one block at a time. Four places to put
-- prose is a page with some prose on it. Somewhere to put anything is a wiki.
--
-- No CHECK on the key here: the set lives in the code that renders it, and a
-- constraint would be a second copy to keep in step. The command refuses an
-- unknown one, which is where a person finds out.
CREATE TABLE page_note (
  key        TEXT PRIMARY KEY,
  body       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;
