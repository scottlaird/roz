-- Which verb starts a pipeline, rather than code knowing it is `write`.
--
-- Closing an action with a subject pull request is not on its own a reason to
-- instantiate a chain: an `investigate` action about someone's pull request
-- ends when you know the answer, and should produce nothing. Only a verb that
-- produces work needing review starts one.
--
-- Keeping that in the vocabulary means a new such verb is a row, the same way
-- a new human-closed verb already is. The steps a chain contains are still
-- the pipeline's business; this column only says whether closing opens one.

ALTER TABLE actionverb ADD COLUMN starts_pipeline INTEGER NOT NULL DEFAULT 0
  CHECK (starts_pipeline IN (0,1));

UPDATE actionverb SET starts_pipeline = 1 WHERE verb = 'write';
