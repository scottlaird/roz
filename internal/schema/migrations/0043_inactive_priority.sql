-- The priority at which a project stops being active work.
--
-- Priorities run 1 to 9, and somewhere down that range a project stops being
-- something anybody is working on and becomes "maybe someday". Its actions
-- are still true -- that is why they were written down -- but in the queue
-- they are things to read and rule out every time, which is the noise the
-- queue exists to remove. Closing them would lose them; snoozing them invents
-- a date nobody believes.
--
-- So the queue leaves out actions whose project's priority is this value or
-- higher, and lists them again the moment the project is reprioritised. It is
-- read at query time, like a blocked project, so there is nothing to release.
--
-- 0 means no cutoff, and is the default: a queue that has never said what its
-- priorities mean keeps showing everything.
ALTER TABLE config ADD COLUMN inactive_priority INTEGER NOT NULL DEFAULT 0
  CHECK (inactive_priority BETWEEN 0 AND 9);
