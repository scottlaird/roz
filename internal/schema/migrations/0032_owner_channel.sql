-- Where a group is reached.
--
-- `roz pr announce` required --channel on every invocation, and the channel
-- was retyped every time. The obvious fix -- default it from the repository --
-- is the wrong grain, which is why github_repo.announce_channel has sat in the
-- schema since 0002 with nothing reading it. A change touching storage should
-- reach the storage channel whichever repository it is in, and a monorepo has
-- no single right answer at all.
--
-- The channel belongs to the reviewer. #136 works out who to ask -- an
-- ordered, coverage-checked list of owners -- so once that is decided, the
-- channel is a table read. This is a lookup, not a rules engine; conflating
-- the two is what the routing issue got wrong before it was split.
--
-- Groups only. A channel is how you reach a team; an individual is reached by
-- naming them, and giving a person a channel is a different concept wearing
-- the same word. Where routing resolves to a person this has no answer, and
-- saying so is better than inventing one -- hence the '/' in the key, which is
-- what distinguishes '@org/storage' from '@alice'.
--
-- A team is per-organisation and already spells its org, so the owner is the
-- key and two organisations naming the same team do not collide. A channel is
-- per-workspace, and the mapping is not one-to-one in either direction: two
-- teams can share a channel, which is why nothing here is unique on it.
--
-- No derivation from the name. '@org/storage' and '#storage-reviews' are
-- different namespaces that happen to correlate, and turning one into the
-- other by string manipulation is the kind of rule that works for eleven teams
-- and then quietly does not.
--
-- Where a pull request was actually announced stays on pr.announced_channel.
-- That is an observation of an act; this is policy, and policy may have
-- changed since.
CREATE TABLE owner_channel (
  -- an owner as CODEOWNERS writes one, restricted to teams: '@org/storage'.
  owner      TEXT PRIMARY KEY CHECK (owner <> '' AND instr(owner, '/') > 0),
  -- as Slack writes one, '#storage-reviews'. Not validated beyond being
  -- something: a workspace can name a channel whatever it likes, and roz never
  -- posts, so a wrong one is a note to a person rather than a failed request.
  channel    TEXT NOT NULL CHECK (channel <> ''),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;
