-- Three more views, and the rule for which questions get seeded.
--
-- 0036 seeded the two the page reads by name, because the code depended on
-- them. These three are seeded for a different reason: the listing pages now
-- have a view picker, and a picker with two entries teaches nobody what a view
-- is for.
--
-- The rule they follow is that a seeded view must be a question roz already
-- answers some other way. Each of these is the definition behind an existing
-- flag, moved somewhere it can be read and edited rather than only obeyed:
--
--   expired_actions     `action list --expired`
--   stalled_projects    `project list --orphaned`
--   unchecked_actions   `action list --sort staleness`, over open actions
--
-- That is what keeps the seed set from becoming taste. A `decisions` view or a
-- `my reviews` view would be somebody's working style, and those belong in the
-- database of the person who wants them -- which is a `roz view add` away, and
-- is the point of the table.
--
-- unchecked_actions is the same rows as open_actions in a different order,
-- which reads like a duplicate and is not: it is the one thing the picker can
-- already do that the ranking argument in #91 asks for. What has nobody looked
-- at is a different question from what matters most, and it has always been a
-- different sort rather than a different filter.
--
-- It is not called stale_actions on purpose. ActionFilter.Stale already means
-- something else -- closed as completed while the pull request is still open --
-- and two names one letter apart for unrelated questions is how somebody reads
-- the wrong list and believes it.
--
-- The filters mirror the flags' SQL rather than approximating it:
--
--   --expired    state = 'snoozed' AND snooze_until IS NOT NULL
--                AND snooze_until < now
--   --orphaned   status NOT IN ('done','retired','superseded')
--                AND snooze_until IS NULL
--                AND id NOT IN (SELECT project_id FROM action
--                               WHERE closed_at IS NULL)
--
-- The last one is a correlated EXISTS in CEL, which converts to SQL whole --
-- so the page can use it without reading every project.
INSERT INTO view (name, entity, filter, sort, description, created_at, updated_at) VALUES
  ('expired_actions', 'action',
   'state == "snoozed" && snooze_until != null && snooze_until < now',
   'priority',
   'Snoozed until a date that has passed. A snooze nobody is watching is how work goes quiet.',
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),

  ('unchecked_actions', 'action',
   'closed_at == null',
   'staleness',
   'Open work, longest un-checked first. The same rows as open_actions, asked in the other order: what nobody has looked at, rather than what matters most.',
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),

  ('stalled_projects', 'project',
   'status != "done" && status != "retired" && status != "superseded" && snooze_until == null && !actions.exists(a, a.closed_at == null)',
   'priority',
   'Live projects with no open action and no snooze: work that is on no surface anyone reads.',
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
