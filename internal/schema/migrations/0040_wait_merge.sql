-- Waiting for somebody else's pull request to merge.
--
-- The predicate already existed and only `merge` named it. But `merge` is
-- rank_class = click with a one-day allowance, because it describes a button
-- *you* press on a pull request *you* own. Pointing it at an upstream pull
-- request gets both halves wrong: the item lands in the queue as a click
-- nobody here can make, and goes overdue the next day.
--
-- The case is ordinary. roz waits on SPANDigital/cel2sql#169 for the fix
-- behind #202, and the honest statement is "blocked until that merges". The
-- workaround was to wait on the *issue* the pull request closes, which works
-- only where the pull request says so; merge the same fix with no closing
-- reference and the wait hangs for ever, which is the failure a wait exists to
-- prevent.
--
-- No wait_days, for the reason wait_ref has none: somebody else's merge is not
-- yours to influence and there is nobody to chase, so an allowance would put a
-- durable item in the queue that nothing can clear.
--
-- requires_pr, so `action add --verb wait_merge` refuses without one -- see
-- #78. The pull request has to be tracked for pr_merged to read anything,
-- which means tracking a repository you do not own; `--because watching` is
-- how that is said, and 0039's sibling change keeps a pipeline off it.
INSERT INTO actionverb
  (verb, label, closes, predicate_key, rank_class, requires_pr, requires_ref,
   requires_owner, requires_issue, starts_pipeline, wait_days, description)
VALUES
  ('wait_merge', 'wait for a merge', 'predicate', 'pr_merged', 'wait',
   1, 0, 0, 0, 0, NULL,
   'Wait for somebody else''s pull request to merge. Closes when it has. Unlike `merge`, which is your click on your own pull request, this has no allowance: their merge is not yours to influence.');
