-- How many times a pull request has been announced.
--
-- announced_at is overwritten by every `pr announce`, so it answers "when were
-- they last told" and nothing answers "how many times have I had to tell
-- them". That second question is the one that matters once the wait clock
-- reads the announcement: chasing a stalled review now buys real patience, and
-- patience granted three times over is not the same situation as patience
-- granted once. Without a count the two are indistinguishable in the data, and
-- the escalating case -- the one where the pings are running out of road -- is
-- exactly the one worth seeing.
--
-- A count rather than a table of announcements. What is wanted is "is this the
-- first time", and a row per announcement would carry a channel and a
-- timestamp that announced_channel and announced_at already hold for the only
-- one anything reads. The log has the full history either way: every
-- announcement is an update to an observed column, so the chases are already
-- there as `announce_count: "1" -> "2"` under sync:slack-manual.
--
-- Observed, like the two columns beside it, and written by the same actor for
-- the same reason: `pr announce` is the standing exception where a person
-- reports what Slack would have.
--
-- Backfilled to 1 where an announcement exists. Zero would say never
-- announced, which is false for every pull request already frozen by one, and
-- would read as a fresh first chase the next time one was sent.
ALTER TABLE pr ADD COLUMN announce_count INTEGER NOT NULL DEFAULT 0
  CHECK (announce_count >= 0);

UPDATE pr SET announce_count = 1 WHERE announced_at IS NOT NULL;
