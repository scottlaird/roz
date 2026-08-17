-- Where a week begins, which turns out to be two settings rather than one.
--
-- The weekly wrap-up needs to know what "this week" covers. The day a week is
-- *labelled* from and the day the review is *done* are different questions,
-- and they disagree for anybody who reviews on a Friday and thinks of weeks as
-- starting on Monday -- which is the normal case rather than an edge one.
--
-- week_start decides what "Week of ..." means in a heading, and nothing else.
-- It is the setting every calendar application has.
--
-- review_day decides the reporting window: the seven days ending at the review,
-- so everything since the last one is covered exactly once. It is a window
-- definition and not a schedule -- nothing fires on it, and the report is run
-- whenever somebody runs it. Making it a schedule is a larger feature and a
-- different issue.
--
-- Names rather than numbers. There is no arithmetic on them at this level, and
-- `week_start: "sunday" -> "monday"` reads in the log where a 0 and a 1 would
-- not. The CHECK is the full set of days: unlike the tracker vocabularies, this
-- one cannot grow.
--
-- Defaults are monday and friday. Monday is ISO-8601's week start and the
-- majority convention; Friday is the day a week's work is usually looked back
-- over. Both are settings precisely because neither is universal.
ALTER TABLE config ADD COLUMN week_start TEXT NOT NULL DEFAULT 'monday'
  CHECK (week_start IN ('monday','tuesday','wednesday','thursday','friday','saturday','sunday'));

ALTER TABLE config ADD COLUMN review_day TEXT NOT NULL DEFAULT 'friday'
  CHECK (review_day IN ('monday','tuesday','wednesday','thursday','friday','saturday','sunday'));
