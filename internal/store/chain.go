package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ChainState is what a pull request's pipeline has actually produced on it, as
// opposed to what it would produce.
//
// The two came apart because a chain is only ever extended by closing a step
// that is already part of one. An action made with `action add` carrying a
// pipeline verb is indistinguishable from a chain step — same verb, same
// predicate, same closing behaviour — and the difference only shows after it
// closes, when nothing follows it and the pull request leaves the queue
// entirely. That is scottlaird/roz#96, and it happens at the worst moment:
// the head closes exactly when the work stops being the author's problem and
// becomes a thing to watch.
//
// Nothing derives this on its own. It is read on demand, by `pr show` so the
// gap is visible before it bites, and by `pr chain` to close it.
type ChainState struct {
	// Pipeline is the chain that applies, empty when none does.
	Pipeline string
	// Source is what named it: the pull request when it overrides, otherwise
	// its repository. The distinction is the point of this type — a reader
	// seeing a repository-level pipeline configured reasonably concludes the
	// chain will happen, and it is exactly that conclusion that is wrong.
	Source string
	// Steps are the pipeline's, in order, each with what fulfils it.
	Steps []ChainStep
}

// ChainStep is one step of the pipeline and whatever stands in for it.
type ChainStep struct {
	Verb string
	Spec string
	// Action is the action fulfilling this step, empty when none does.
	Action string
	// Open says that action is still open, so it can block what follows.
	Open bool
	// Satisfied says the pull request already meets the step's predicate, so
	// there is nothing to do and nothing to create. Distinct from having an
	// action: a pull request that was never a draft has no undraft step and
	// needs none.
	Satisfied bool
}

// Missing reports whether the step has neither an action nor a reason not to
// need one.
func (s ChainStep) Missing() bool { return s.Action == "" && !s.Satisfied }

// Missing names the steps nothing covers, in pipeline order.
func (c *ChainState) Missing() []ChainStep {
	var missing []ChainStep
	for _, s := range c.Steps {
		if s.Missing() {
			missing = append(missing, s)
		}
	}
	return missing
}

// Applies reports whether any pipeline reaches this pull request. False is a
// legitimate state — a repository whose pull requests are not chained — and
// is why the gap cannot simply be reported as an exception.
func (c *ChainState) Applies() bool { return c.Pipeline != "" }

// Summary is the one-line answer to "will this pull request be carried to
// merge", which is what `pr show` needs and what its pipeline column could
// never say: that column holds the override, so it reads "-" both for a pull
// request following its repository's chain and for one following nothing.
func (c *ChainState) Summary() string {
	if !c.Applies() {
		return "none"
	}
	where := c.Pipeline
	if c.Source != "" {
		where = fmt.Sprintf("%s from %s", c.Pipeline, c.Source)
	}
	missing := c.Missing()
	if len(missing) == 0 {
		return where
	}
	verbs := make([]string, len(missing))
	for i, s := range missing {
		verbs[i] = s.Verb
	}
	return fmt.Sprintf("%s; missing %s", where, commaList(verbs))
}

// ChainOf reads what a pull request's pipeline has produced on it.
//
// Steps are matched to actions by verb rather than by position, so a
// half-built chain can be told apart from a whole one however it was built —
// by hand, by the cascade, or both. Repeats are counted rather than collapsed:
// a pipeline asking for two reviews and holding one action for them is missing
// the second.
//
// Any action with the verb counts, closed as well as open, and closed for any
// reason. A step somebody did and a step somebody dropped are both decisions
// already taken, and re-creating either would overrule a person with a rule.
func (t *Tx) ChainOf(ctx context.Context, prID string) (*ChainState, error) {
	pr, err := t.LoadPR(ctx, prID)
	if err != nil {
		return nil, err
	}
	repo, _, err := ParsePRKey(prID)
	if err != nil {
		return nil, err
	}
	r, err := t.LoadGitHubRepo(ctx, repo)
	if err != nil {
		return nil, err
	}

	name, source := effectivePipeline(pr, r, repo)
	state := &ChainState{Pipeline: name, Source: source}
	if name == "" {
		return state, nil
	}

	pipeline, err := t.LoadPipeline(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%s names pipeline %q, which does not exist", source, name)
		}
		return nil, err
	}

	existing, err := t.actionsOnPR(ctx, prID)
	if err != nil {
		return nil, err
	}

	for _, step := range pipeline.Steps {
		verb, err := t.LoadVerb(ctx, step.Verb)
		if err != nil {
			return nil, fmt.Errorf("reading step %q of pipeline %s: %w", step.Verb, name, err)
		}
		found := ChainStep{Verb: step.Verb, Spec: step.Spec}
		if a := takeByVerb(existing, step.Verb); a != nil {
			found.Action, found.Open = a.ID, a.IsOpen()
		} else {
			found.Satisfied = satisfied(verb, pr)
		}
		state.Steps = append(state.Steps, found)
	}
	return state, nil
}

// effectivePipeline names the chain a pull request runs and what said so.
//
// The pull request's own wins when it has one; otherwise the repository's,
// read here rather than copied when the pull request was tracked, so a
// repository whose policy changes carries the pull requests that never claimed
// an exception to it.
func effectivePipeline(pr *PR, r *GitHubRepo, repo string) (name, source string) {
	if pr.Pipeline.Valid {
		return pr.Pipeline.String, pr.ID
	}
	if r.Pipeline.Valid {
		return r.Pipeline.String, repo
	}
	return "", ""
}

// actionsOnPR is every action whose subject this pull request is, closed ones
// included, oldest first. LinkedActions is the open half of the same query;
// what has already been done is exactly what must not be created again.
func (t *Tx) actionsOnPR(ctx context.Context, prID string) ([]*Action, error) {
	return t.loadActions(ctx, `
		SELECT %s FROM action a
		WHERE a.id IN (SELECT action_id FROM action_pr WHERE pr_id = ? AND role = 'subject')
		ORDER BY a.n`, prID)
}

// takeByVerb consumes the oldest unmatched action with a verb, so a pipeline
// naming a verb twice is answered by two actions rather than by one twice.
func takeByVerb(actions []*Action, verb string) *Action {
	for i, a := range actions {
		if a != nil && a.Verb == verb {
			actions[i] = nil
			return a
		}
	}
	return nil
}

// commaList renders a short list the way a sentence would.
func commaList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// ChainResult is what instantiating produced, so the command can report it.
type ChainResult struct {
	// State is the chain as it was read, before anything was created.
	State *ChainState
	// Created are the steps written, in pipeline order.
	Created []*Action
	// BlockedBy is what each created step waits on, by index, empty where it
	// is ready straight away.
	BlockedBy []string
}

// InstantiateChain creates the pipeline steps a pull request is missing.
//
// It exists because closing a hand-made head starts nothing: the cascade
// extends a chain, and there is no chain to extend. The manual repair — build
// the remaining steps and add the blockers between them — works, and has to be
// remembered every single time, which means the cases where it is forgotten
// are by definition the ones nobody sees.
//
// # What it does not do
//
// It never creates a step something already covers, so running it twice
// produces nothing the second time and a half-built chain is completed rather
// than duplicated.
//
// It never writes the steps that already happened. Recording them as closed so
// the chain looks whole would put closes in the log that nobody performed, and
// the log's whole claim is that it is derived rather than authored. It would
// also mint identifiers out of chronological order, in a scheme whose numbers
// are quoted in commit messages and in conversation. So: append only.
//
// It never touches an action that already exists — not even to block it behind
// a step created now. Somebody who added a single merge action for a pull
// request they are lightly tracking asked for one item, and rearranging what
// they already have is the "do not adopt loose actions" caveat on #96. Each
// created step is blocked by whatever fulfils the step immediately before it,
// which may be an action created here or one that was already open; the edge
// is only ever added to the new row.
//
// Immediately before, rather than the last open thing anywhere ahead of it.
// A pipeline missing its first and last steps around an open middle one —
// undraft and merge around a live wait_review — would otherwise chain merge to
// the send_for_review created beside it and skip the wait entirely, freeing
// merge while the review it depends on is still open.
//
// Three phases, for the reason CloseAction has them: identifiers are allocated
// on their own connection, so a transaction holding the write lock would
// deadlock against it. Read, allocate, write.
func (s *Store) InstantiateChain(ctx context.Context, actor Actor, prID string) (*ChainResult, error) {
	state, planned, err := s.planChain(ctx, actor, prID)
	if err != nil {
		return nil, err
	}
	result := &ChainResult{State: state}
	if len(planned) == 0 {
		return result, nil
	}

	steps := make([]*Action, len(planned))
	for i, step := range planned {
		a := NewAction(step.title, step.verb)
		if step.requiresOwner && step.spec != "" {
			a.WaitingFor = sql.NullString{String: step.spec, Valid: true}
		}
		if err := s.AllocateAction(ctx, a); err != nil {
			return nil, err
		}
		steps[i] = a
	}

	return s.applyChain(ctx, actor, result, planned, steps, prID)
}

// planChain reads which steps are missing and what each would be. It writes
// nothing, and its transaction is finished with before an identifier is
// allocated.
func (s *Store) planChain(ctx context.Context, actor Actor, prID string) (*ChainState, []chainPlan, error) {
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	state, err := tx.ChainOf(ctx, prID)
	if err != nil {
		return nil, nil, err
	}
	if !state.Applies() {
		return nil, nil, fmt.Errorf(
			"no pipeline reaches %s: set one with `roz repo set --pipeline` or `roz pr set --pipeline`",
			prID)
	}

	var planned []chainPlan
	for at, step := range state.Steps {
		if !step.Missing() {
			continue
		}
		verb, err := tx.LoadVerb(ctx, step.Verb)
		if err != nil {
			return nil, nil, fmt.Errorf("reading step %q of pipeline %s: %w",
				step.Verb, state.Pipeline, err)
		}
		planned = append(planned, chainPlan{
			at: at,
			plannedStep: plannedStep{
				verb:          step.Verb,
				spec:          step.Spec,
				requiresOwner: verb.RequiresOwner,
				title:         stepTitle(verb, PipelineStep{Verb: step.Verb, Spec: step.Spec}, prID),
			},
		})
	}
	return state, planned, nil
}

// chainPlan is a step to create and where in the pipeline it sits, which is
// what says who blocks it. The cascade needs no such thing: it always
// instantiates a whole tail, so position and order are the same fact.
type chainPlan struct {
	plannedStep
	at int
}

// applyChain writes the steps and the blockers between them, in one unit of
// work.
func (s *Store) applyChain(ctx context.Context, actor Actor, result *ChainResult,
	planned []chainPlan, steps []*Action, prID string) (*ChainResult, error) {

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Re-read: the plan was made in a transaction that has since ended, and
	// something may have created one of these in between. Creating a
	// duplicate merge step is the one failure this command must not have.
	state, err := tx.ChainOf(ctx, prID)
	if err != nil {
		return nil, err
	}
	if !sameMissing(result.State, state) {
		return nil, fmt.Errorf("%s changed while reading it; try again", prID)
	}

	blockers := chainBlockers(state, planned, steps)
	project, why := inheritedFrom(ctx, tx, state)
	if err := tx.instantiateAfter(ctx, steps, planned, prID, blockers, project, why); err != nil {
		return nil, err
	}

	result.Created = steps
	result.BlockedBy = blockers

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// chainBlockers names what each created step waits on: whatever fulfils the
// step immediately before it in the pipeline, created here or already open.
//
// Empty means ready straight away, which is the answer for the first step and
// for any step whose predecessor is closed or was never needed. A closed
// action frees nothing, so hanging a step behind one would leave it blocked by
// something already over.
func chainBlockers(state *ChainState, planned []chainPlan, steps []*Action) []string {
	// Whichever action stands at each position, once the new ones are in
	// place, but only while it is open — an edge to a closed row is a wait
	// that never ends.
	open := make(map[int]string, len(state.Steps))
	for at, step := range state.Steps {
		if step.Open {
			open[at] = step.Action
		}
	}
	for i, plan := range planned {
		open[plan.at] = steps[i].ID
	}

	blockers := make([]string, len(planned))
	for i, plan := range planned {
		blockers[i] = open[plan.at-1]
	}
	return blockers
}

// sameMissing reports whether two readings of a chain are missing the same
// steps, which is the only part of it this command acts on.
func sameMissing(before, after *ChainState) bool {
	was, now := before.Missing(), after.Missing()
	if before.Pipeline != after.Pipeline || len(was) != len(now) {
		return false
	}
	for i := range was {
		if was[i].Verb != now[i].Verb || was[i].Spec != now[i].Spec {
			return false
		}
	}
	return true
}

// inheritedFrom reads the project and reason the existing steps carry, so a
// chain completed after the fact lands in the same project as the work it
// belongs to. The cascade copies both from the action it closed; here there is
// no such action, so the nearest thing is what the pull request's own steps
// already say.
//
// The last one wins, which is arbitrary among steps that agree and harmless
// among steps that do not: this is a default for rows that would otherwise
// have none, and `action set` moves them.
func inheritedFrom(ctx context.Context, tx *Tx, state *ChainState) (project sql.NullString, why string) {
	for _, s := range state.Steps {
		if s.Action == "" {
			continue
		}
		a, err := tx.LoadAction(ctx, s.Action)
		if err != nil {
			continue
		}
		if a.ProjectID.Valid {
			project = a.ProjectID
		}
		if a.Why != "" {
			why = a.Why
		}
	}
	return project, why
}

// instantiateAfter writes the steps, each blocked by whatever chainBlockers
// named for it.
//
// It is instantiate with the edges supplied rather than implied. The cascade
// always creates a whole tail, so "the one before it" is the previous element
// of its own slice; here the chain is being joined partway along and a
// predecessor may be an action that was already there.
func (t *Tx) instantiateAfter(ctx context.Context, steps []*Action, planned []chainPlan,
	subject string, blockers []string, project sql.NullString, why string) error {

	repo, _, repoErr := ParsePRKey(subject)
	for i, a := range steps {
		a.ProjectID = project
		a.Why = why
		if err := t.Insert(ctx, a); err != nil {
			return err
		}
		if err := t.LinkPR(ctx, a, subject, RoleSubject); err != nil {
			return err
		}

		if by := blockers[i]; by != "" {
			blocker, err := t.LoadAction(ctx, by)
			if err != nil {
				return err
			}
			if err := t.AddBlocker(ctx, blocker, a); err != nil {
				return err
			}
		}

		if spec := planned[i].spec; spec != "" && !planned[i].requiresOwner {
			if repoErr != nil {
				return fmt.Errorf("%s waits for %s, but %q names no repository",
					a.ID, spec, subject)
			}
			if err := t.AddPendingRef(ctx, PendingRef{
				ActionID: a.ID, RepoID: repo, Kind: RefTag, Spec: spec,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
