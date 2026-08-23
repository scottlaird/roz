-- Waiting on something roz has no way to see.
--
-- Every other wait verb is backed by a predicate over something observable:
-- wait_review and wait_merge need a pull request, wait_ref a ref, wait_issue a
-- tracker issue. There was nothing for the outage, the unanswered question, or
-- the other team's work — cases that come up whenever something outside stops a
-- chain.
--
-- Each of those got `investigate`, which misreports the item three ways: it is
-- a session rank class, so the queue offers it as work when there is none to
-- do; it carries no allowance, so nothing notices a wait that has run long; and
-- it reads as something the owner can advance, which is exactly what it is not.
--
-- closes = human, and deliberately no predicate. The point is that there is
-- nothing to observe. Inventing one -- polling a status page, say -- would make
-- the verb narrower than the need and put roz in the business of monitoring
-- third parties.
--
-- rank_class = wait, so the queue stops offering it and other actions can hide
-- behind it without the parent claiming to be actionable.
--
-- wait_days = 3, where wait_ref has none. The reasoning that leaves a release
-- gate without an allowance is that nobody can be chased about a release date;
-- here there is somebody to chase by construction -- that is what makes it
-- manual -- and nothing will ever close this on its own, so a wait that has run
-- long is more in need of being noticed than a polled one, not less.
--
-- It requires nothing: no pull request, no ref, no issue. That is the whole
-- point of it, and it is why this cannot be expressed by relabelling an
-- existing verb.
INSERT INTO actionverb
  (verb, label, closes, predicate_key, rank_class, requires_pr, requires_ref,
   requires_owner, requires_issue, starts_pipeline, wait_days, description)
VALUES
  ('wait_on', 'wait on', 'human', NULL, 'wait',
   0, 0, 0, 0, 0, 3,
   'Wait for something roz cannot see: an outage, an answer from a person, another team''s work. Closed by hand, because there is nothing to poll. Not for a pull request that is merely slow -- wait_review closes itself, and this would not.');
