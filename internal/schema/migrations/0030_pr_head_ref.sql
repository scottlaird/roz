-- The branch a pull request is *from*.
--
-- base_ref -- the branch it targets -- has been synced since the start, and
-- the head branch was not stored at all, so there was nothing to match it
-- against. That is why stacked_on was never set: the column existed, the
-- listing filtered on it, and no query could derive it.
--
-- head_sha is not a substitute. Two pull requests in a chain have different
-- heads by construction, and what a child targets is the parent's branch
-- name rather than its commit.
--
-- Observed, like the rest of what GitHub says about a pull request.
ALTER TABLE pr ADD COLUMN head_ref TEXT;

-- The lookup the resolver does: given a base_ref, which tracked pull request
-- in the same repository has that head. Partial because an unsynced row has
-- no head to be found by.
CREATE INDEX pr_head_ref ON pr(repo, head_ref) WHERE head_ref IS NOT NULL;
