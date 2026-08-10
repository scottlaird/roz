-- The verb vocabulary, from the design sketch.
--
-- Each verb says how it closes. A predicate verb names a function in the
-- code's registry, resolved by name and never by expression: encoding the
-- predicates as data would make this table a programming language. A verb
-- whose predicate_key has no registered function is refused at startup.
--
-- rank_class drives both the sort and the render class, so adding a verb does
-- not mean editing the ordering logic, and a class that was never styled is
-- not expressible.
--
-- Adding a human-closed verb later is a row and no deploy, which is the
-- common case and should be cheap. Adding a predicate verb is a row plus a
-- function, which is deliberately not.
--
-- Never delete a row from this table: closed actions and log entries
-- reference verbs that may since have been retired. Set active = 0 instead.

INSERT INTO actionverb (verb, label, closes, predicate_key, rank_class, requires_pr, description) VALUES
  -- Predicate-closed. These flow through on their own.
  ('undraft',          'un-draft',          'predicate', 'pr_not_draft',      'click',   1,
   'Take the pull request out of draft.'),
  ('send_for_review',  'send for review',   'predicate', 'pr_announced',      'click',   1,
   'Announce it where reviewers will see it. The announcement is the one signal GitHub cannot supply.'),
  ('wait_review',      'wait for review',   'predicate', 'pr_approved',       'wait',    1,
   'Nothing to do but wait. Closes when the review decision is APPROVED.'),
  ('address_comments', 'address comments',  'predicate', 'pr_threads_clear',  'session', 1,
   'Deal with review threads. Closes when none are unresolved against the current head.'),
  ('rebase',           'rebase',            'predicate', 'pr_mergeable',      'click',   1,
   'Bring it up to date. Closes when the merge state is neither BEHIND nor DIRTY.'),
  ('merge',            'merge',             'predicate', 'pr_merged',         'click',   1,
   'One click, once everything else is done.'),

  -- Human-closed. These are the items worth spending attention on, and the
  -- only ones that reach the queue as thinking work.
  ('decide',           'decide',            'human',     NULL,                'decide',  0,
   'A judgement that has to be made before anything else can move.'),
  ('write',            'write',             'human',     NULL,                'session', 0,
   'Actual work. Usually ends with a pull request.'),
  ('announce',         'announce',          'human',     NULL,                'click',   0,
   'Tell someone something.'),
  ('run',              'run',               'human',     NULL,                'click',   0,
   'Run a command or a job and see what it says.'),
  ('file',             'file',              'human',     NULL,                'click',   0,
   'Raise a ticket or an issue somewhere else.'),
  ('investigate',      'investigate',       'human',     NULL,                'session', 0,
   'Find out what is going on. Closes when you know.'),

  -- review is human-closed for now, though the sketch has it closing on a
  -- predicate. It needs to know that *we* submitted a review, which needs
  -- both the viewer's identity and pull requests tracked because they are
  -- assigned to us rather than authored by us. Neither exists yet.
  ('review',           'review',            'human',     NULL,                'session', 1,
   'Review someone else''s pull request.');
