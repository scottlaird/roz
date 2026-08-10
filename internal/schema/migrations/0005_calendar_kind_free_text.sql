-- Drops the CHECK on calendar_window.kind, so a new kind costs nothing.
--
-- The four kinds were a closed set, and adding a fifth meant rebuilding this
-- table -- SQLite cannot alter a CHECK in place. That is a lot of ceremony
-- for a value nothing branches on: kind is stored, filtered and displayed,
-- and it is capacity that decides whether a block reads as unavailable. Doing
-- the rebuild once now is cheaper than doing it whenever a kind is wanted.
--
-- capacity keeps its CHECK. That one is a closed set with meaning attached:
-- the sort reads it, and none/reduced/full is the distinction between an
-- oncall week and a fortnight away.
--
-- The CLI still offers the familiar four in its help, so a typo is caught
-- where it is made rather than becoming a row nobody meant.

CREATE TABLE calendar_window_new (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,             -- free text; see above
  label      TEXT NOT NULL,
  starts_on  TEXT NOT NULL,             -- 'YYYY-MM-DD', inclusive
  ends_on    TEXT NOT NULL,             -- 'YYYY-MM-DD', INCLUSIVE — see the entry note
  capacity   TEXT NOT NULL CHECK (capacity IN ('none','reduced','full')),
  note       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  CHECK (ends_on >= starts_on)
) STRICT;

INSERT INTO calendar_window_new (id, kind, label, starts_on, ends_on, capacity, note, created_at)
SELECT id, kind, label, starts_on, ends_on, capacity, note, created_at
FROM calendar_window;

DROP TABLE calendar_window;
ALTER TABLE calendar_window_new RENAME TO calendar_window;
