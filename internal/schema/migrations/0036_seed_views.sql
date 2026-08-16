-- The page's own questions, as views.
--
-- The status page asks a handful of things: which projects are live, which
-- actions are open, what is in the queue, what the queue is deliberately
-- leaving out. Each of those was a filter compiled into Go, so changing one
-- meant a rebuild -- which is exactly what the view table exists to stop.
--
-- Two of them convert, and are seeded here. The page reads them by name, so
-- what "live projects" means is now a row somebody can edit.
--
-- The rest cannot be views, and the reasons are worth writing down rather
-- than rediscovering:
--
--   the queue        `--unblocked` is a ranking, not a predicate. It reads the
--                    verb's rank class through actionverb, and whether the
--                    action's project is blocked, neither of which is a column
--                    of the action. #169 settled that a ranking is not a
--                    column sort; the same argument applies here.
--   waiting          the same machinery, inverted.
--   the calendar     a window within N days of today needs date arithmetic on
--                    `now`, and a filter has a clock but cannot do sums with
--                    it.
--   everything else  the page loads every action, project, pull request and
--                    issue to caption links with, which is the absence of a
--                    filter rather than a filter.
--
-- Seeded rather than created by the page on first run. A view somebody can
-- edit has to be a row from the start: one conjured at startup would come
-- back the next time it was deleted, which is the opposite of the point.
INSERT INTO view (name, entity, filter, sort, description, created_at, updated_at) VALUES
  ('open_projects', 'project',
   'status != "done" && status != "retired" && status != "superseded"',
   'priority',
   'Live work, blocked included. A blocked project is still live, and hiding one is how SL22 sat unseen for a session.',
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),

  ('open_actions', 'action',
   'closed_at == null',
   'priority',
   'Everything still to do, in queue order — which is more than the queue shows, since the queue folds away what is blocked or waiting.',
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
   strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
