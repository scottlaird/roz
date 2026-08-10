-- address_comments closes when there are no unresolved review threads newer
-- than the current head. Nothing recorded that, so the verb could not close.
--
-- GitHub reports isOutdated on a thread, meaning it hangs off a commit that
-- is no longer the head. "Unresolved and not outdated" is what the predicate
-- wants, and is counted here rather than stored per thread: the queue needs
-- to know whether there is anything to address, not what.
--
-- NULL means never synced, which is not the same as zero.
ALTER TABLE pr ADD COLUMN unresolved_threads INTEGER;
