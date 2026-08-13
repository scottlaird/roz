-- Who a pull request needed, as of when it was last worked out.
--
-- A wait_review action can say a pull request is waiting for review. It could
-- not say *who* it waits for, because nothing here knew: sync asks GitHub for
-- a pull request's state, not for its file list, and nothing read CODEOWNERS.
-- "Waiting on review" is what you already knew when you created the action;
-- "waiting on @org/storage since Tuesday" is what says whether to chase it.
--
-- An observation, not a fact about the repository. Ownership is per-directory
-- and CODEOWNERS changes underneath a long-lived pull request, so what was
-- required when the review was requested is not necessarily what the file says
-- today. Stored the way every other observation is: an observed column whose
-- changes the diff logs, so the log carries when each answer was true.
--
-- The set of owners, not the mapping of every path to its owner. A large pull
-- request touches enough paths to name half an organisation, and the question
-- anybody asks is "who does this need", never "who owns line 40 of the
-- generated mock". The per-path detail is a live read away for anyone who
-- wants it -- `roz codeowners --pr` -- and belongs in a command rather than in
-- a column that would be mostly noise.
--
-- JSON, sorted, matching reviewer_teams and approvals beside it. Empty is
-- ambiguous on its own -- owned by nobody, or never worked out -- so
-- owners_head below is what tells them apart: NULL there means nothing has
-- ever derived this, and a repository with no CODEOWNERS gets '[]' with a head
-- recorded.
ALTER TABLE pr ADD COLUMN required_owners TEXT NOT NULL DEFAULT '[]'
  CHECK (json_valid(required_owners));

-- The head this was worked out against.
--
-- What makes the derivation skippable. Which files a pull request touches
-- changes only when the pull request does, so a poll re-derives only where the
-- head has moved since -- and the read it saves is the expensive one, a
-- paginated file list plus a CODEOWNERS fetch per pull request.
--
-- Deliberately not last_synced_at. That moves on every poll and would make
-- this re-derive every time; the head is the thing the answer depends on.
ALTER TABLE pr ADD COLUMN owners_head TEXT;
