package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CloseRequest is what a caller wants closing an action to do.
type CloseRequest struct {
	// ID is the action to close.
	ID string
	// Reason is why, from ClosedReasons. Empty means completed.
	Reason string
	// PR links a pull request as the action's subject before closing, for
	// the common case of finishing the work and recording what it produced
	// in one act. Empty leaves any existing link alone.
	PR string
}

// CloseResult is what closing did, so a command can report it. Everything in
// it happened under one correlation id.
type CloseResult struct {
	Closed *Action
	// Created are the pipeline steps instantiated, in order.
	Created []*Action
	// Skipped are the steps that were already true, by verb.
	Skipped []string
	// Unblocked are the actions that became ready.
	Unblocked []*Action
	// Unhidden are the actions that were folded out of the queue behind this
	// one and are now back in it.
	Unhidden []*Action
	// StoodDown are the actions an exception raised about this one, closed
	// because what they were about is over.
	StoodDown []*Action
	// Pipeline is the chain that was instantiated, empty if none was.
	Pipeline string
}

// CloseAction closes an action and everything that follows from closing it:
// the pipeline it opens, the dependents it frees, and the actions that were
// hidden behind it.
//
// It runs in three phases, and the order is forced rather than chosen.
// Identifiers for the new actions have to be allocated between reading and
// writing, because allocation writes on its own connection — a failed insert
// must still consume the number — and a transaction holding the write lock
// would deadlock against it. So: read what to do, allocate, then write.
//
// A number allocated for a step that then fails to insert is spent. That is
// the same trade `action add` makes, and the reason identifiers may have gaps
// but are never reused.
func (s *Store) CloseAction(ctx context.Context, actor Actor, req CloseRequest) (*CloseResult, error) {
	reason := req.Reason
	if reason == "" {
		reason = ClosedCompleted
	}
	if !validClosedReason(reason) {
		return nil, fmt.Errorf("%q is not a reason to close: use one of %v", reason, ClosedReasons)
	}

	plan, err := s.planClose(ctx, actor, req)
	if err != nil {
		return nil, err
	}

	steps := make([]*Action, len(plan.steps))
	for i, step := range plan.steps {
		a := NewAction(step.title, step.verb)
		a.ProjectID = plan.action.ProjectID
		a.Why = plan.action.Why
		if err := s.AllocateAction(ctx, a); err != nil {
			return nil, err
		}
		steps[i] = a
	}

	return s.applyClose(ctx, actor, plan, steps, reason)
}

// closePlan is what was decided from reading, before anything was written.
type closePlan struct {
	action   *Action
	subject  string
	pipeline string
	steps    []plannedStep
	skipped  []string
}

type plannedStep struct {
	verb string
	// spec is what the step waits for, where the verb needs telling. It
	// becomes a pending gate rather than a wait: what it resolves to is a fact
	// about the repository that has to be read first.
	spec  string
	title string
}

// planClose reads what closing will do. It writes nothing, and its
// transaction is finished with before an identifier is allocated.
func (s *Store) planClose(ctx context.Context, actor Actor, req CloseRequest) (*closePlan, error) {
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	a, err := tx.LoadAction(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if !a.IsOpen() {
		return nil, fmt.Errorf("%s is already %s", a.ID, a.State)
	}

	plan := &closePlan{action: a}

	plan.subject, err = subjectOf(ctx, tx, a, req.PR)
	if err != nil {
		return nil, err
	}
	if plan.subject == "" {
		return plan, nil
	}

	verb, err := tx.LoadVerb(ctx, a.Verb)
	if err != nil {
		return nil, err
	}
	if !verb.StartsPipeline {
		return plan, nil
	}
	// Abandoning the work does not produce the work that would have followed
	// it. Only completing it does.
	if req.Reason != "" && req.Reason != ClosedCompleted {
		return plan, nil
	}

	if err := plan.readPipeline(ctx, tx); err != nil {
		return nil, err
	}
	return plan, nil
}

// subjectOf resolves which pull request the action is about: the one being
// linked now, or the one already linked.
func subjectOf(ctx context.Context, tx *Tx, a *Action, linking string) (string, error) {
	if linking != "" {
		if _, err := tx.LoadPR(ctx, linking); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("%s is not tracked; `roz pr track` it first", linking)
			}
			return "", err
		}
		return linking, nil
	}
	subject, _, err := tx.SubjectPR(ctx, a.ID)
	return subject, err
}

// readPipeline fills in the steps to instantiate, dropping those the pull
// request already satisfies.
//
// Skipping is what keeps the queue honest about work that is already done:
// where pull requests are not created as drafts there is nothing to undraft,
// and an action complete before it exists is noise. A step whose predicate
// cannot be evaluated is kept — absence is not completion, so an unsynced
// pull request produces the whole chain rather than none of it.
func (p *closePlan) readPipeline(ctx context.Context, tx *Tx) error {
	repo, _, err := ParsePRKey(p.subject)
	if err != nil {
		return err
	}
	pr, err := tx.LoadPR(ctx, p.subject)
	if err != nil {
		return err
	}
	r, err := tx.LoadGitHubRepo(ctx, repo)
	if err != nil {
		return err
	}

	// The pull request's own chain wins when it has one; otherwise the
	// repository's, read here rather than copied when the pull request was
	// tracked, so a repository whose policy changes carries the pull requests
	// that never claimed an exception to it.
	//
	// named is whichever said so, and is what an error should blame.
	chain, named := r.Pipeline, repo
	if pr.Pipeline.Valid {
		chain, named = pr.Pipeline, p.subject
	}
	if !chain.Valid {
		return nil
	}

	pipeline, err := tx.LoadPipeline(ctx, chain.String)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s names pipeline %q, which does not exist",
				named, chain.String)
		}
		return err
	}
	p.pipeline = pipeline.Name

	for _, step := range pipeline.Steps {
		verb, err := tx.LoadVerb(ctx, step.Verb)
		if err != nil {
			return fmt.Errorf("reading step %q of pipeline %s: %w", step.Verb, pipeline.Name, err)
		}
		if satisfied(verb, pr) {
			p.skipped = append(p.skipped, step.Verb)
			continue
		}
		p.steps = append(p.steps, plannedStep{
			verb:  step.Verb,
			spec:  step.Spec,
			title: stepTitle(verb, step, p.subject),
		})
	}
	return nil
}

// stepTitle names what the step is for.
//
// A step that waits for a release is not about the pull request, so titling it
// "wait for a ref owner/repo#1" would name the wrong thing entirely. It is
// about the repository and the release it is counting to.
func stepTitle(verb *ActionVerb, step PipelineStep, subject string) string {
	if step.Spec != "" {
		repo, _, err := ParsePRKey(subject)
		if err == nil {
			return fmt.Sprintf("%s %s in %s", verb.Label, step.Spec, repo)
		}
		return fmt.Sprintf("%s %s", verb.Label, step.Spec)
	}
	return fmt.Sprintf("%s %s", verb.Label, subject)
}

// satisfied reports whether a step is already true of the pull request, and
// so has nothing to do.
//
// Only the pull request is supplied, because a step being instantiated has no
// ref wait yet — nothing has had the chance to say what it would wait for. A
// predicate reading anything else therefore answers false and the step is
// created, which is the right way round: skipping is for steps demonstrably
// already true.
func satisfied(verb *ActionVerb, pr *PR) bool {
	predicate, ok := verb.Predicate()
	return ok && predicate(Facts{PR: pr})
}

// applyClose writes everything the plan decided, in one unit of work.
func (s *Store) applyClose(ctx context.Context, actor Actor, plan *closePlan, steps []*Action, reason string) (*CloseResult, error) {
	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Reload: the plan was read in a transaction that has since ended.
	a, err := tx.LoadAction(ctx, plan.action.ID)
	if err != nil {
		return nil, err
	}
	if !a.IsOpen() {
		return nil, fmt.Errorf("%s is already %s", a.ID, a.State)
	}

	// The verb decides what the plan instantiated, so a change to it between
	// reading and writing would apply the wrong chain.
	if plan.action.Verb != a.Verb {
		return nil, fmt.Errorf("%s changed verb while closing it; try again", a.ID)
	}

	result := &CloseResult{Pipeline: plan.pipeline, Skipped: plan.skipped}

	if err := linkSubjectIfNeeded(ctx, tx, a, plan.subject); err != nil {
		return nil, err
	}
	if err := closeRecord(ctx, tx, a, reason); err != nil {
		return nil, err
	}
	result.Closed = a

	result.Created, err = tx.instantiate(ctx, steps, plan.steps, plan.subject)
	if err != nil {
		return nil, err
	}
	if result.Unblocked, err = tx.freeDependents(ctx, a.ID); err != nil {
		return nil, err
	}
	if result.Unhidden, err = tx.unhide(ctx, a.ID); err != nil {
		return nil, err
	}
	if result.StoodDown, err = tx.standDownChases(ctx, a.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// linkSubjectIfNeeded records the pull request the work produced, when
// closing named one that was not already linked.
func linkSubjectIfNeeded(ctx context.Context, tx *Tx, a *Action, subject string) error {
	if subject == "" {
		return nil
	}
	existing, ok, err := tx.SubjectPR(ctx, a.ID)
	if err != nil {
		return err
	}
	if ok {
		if existing != subject {
			return fmt.Errorf("%s is already about %s", a.ID, existing)
		}
		return nil
	}
	return tx.LinkPR(ctx, a, subject, RoleSubject)
}

// closeRecord moves the action itself, setting the state and the pair of
// columns the schema couples to it.
func closeRecord(ctx context.Context, tx *Tx, a *Action, reason string) error {
	after := a.Clone()
	after.State = stateForReason(reason)
	after.ClosedAt = sql.NullString{String: tx.at, Valid: true}
	after.ClosedReason = sql.NullString{String: reason, Valid: true}

	if _, err := tx.Update(ctx, a, after); err != nil {
		return err
	}
	*a = *after
	return nil
}

// stateForReason maps why an action closed onto how it closed.
//
// Only completing something makes it done. Superseded, dropped and obsolete
// are all abandonment, whatever prompted them, and the schema keeps two
// columns precisely so an abandoned item cannot pass for a finished one.
func stateForReason(reason string) string {
	if reason == ClosedCompleted {
		return ActionDone
	}
	return ActionDropped
}

// instantiate writes the pipeline's actions, each blocked by the one before
// it, and each about the same pull request.
//
// Chaining them at creation is what makes the rest mechanical: closing a step
// frees the next through the same unblocking every other action gets, so
// there is no separate notion of "advancing a pipeline" to keep correct.
func (t *Tx) instantiate(ctx context.Context, steps []*Action, planned []plannedStep,
	subject string) ([]*Action, error) {

	repo, _, repoErr := ParsePRKey(subject)
	for i, a := range steps {
		if err := t.Insert(ctx, a); err != nil {
			return nil, err
		}
		if err := t.LinkPR(ctx, a, subject, RoleSubject); err != nil {
			return nil, err
		}
		if i > 0 {
			if err := t.AddBlocker(ctx, steps[i-1], a); err != nil {
				return nil, err
			}
		}

		// A step that waits for a release becomes a gate rather than a wait.
		// What it resolves to is a fact about the repository, and reading that
		// here would mean a network call inside closing an action.
		if spec := planned[i].spec; spec != "" {
			if repoErr != nil {
				return nil, fmt.Errorf("%s waits for %s, but %q names no repository",
					a.ID, spec, subject)
			}
			if err := t.AddPendingRef(ctx, PendingRef{
				ActionID: a.ID, RepoID: repo, Kind: RefTag, Spec: spec,
			}); err != nil {
				return nil, err
			}
		}
	}
	return steps, nil
}

// standDownChases closes the actions an exception raised about this one.
//
// A chase is derived from another action's state, so it must not be able to
// outlive it: a `decide` raised because a wait went past its allowance is
// still telling somebody to nudge a reviewer about a pull request that has
// since been approved. It is a dead item in the queue that has to be read and
// ruled out by hand, which is the cost the queue exists to avoid.
//
// In the same cascade that frees dependents, and for the same reason: closing
// is where everything that follows from closing happens, and a second command
// to tidy up after it is a second command somebody has to remember.
//
// Obsolete rather than completed. The chase records a nudge that never
// happened, and calling it completed would quietly inflate any count of how
// often chasing was needed. Whatever the chase carries — its title, its why,
// anything it later grows — is kept: closing an action does not discard it.
//
// The raised_action row stays. The condition cannot recur for an action that
// is closed, so there is nothing for it to guard against, and it is a record
// of what the queue once said.
func (t *Tx) standDownChases(ctx context.Context, id string) ([]*Action, error) {
	chases, err := t.loadActions(ctx, `
		SELECT %s FROM action a
		JOIN raised_action r ON r.action_id = a.id
		WHERE a.closed_at IS NULL
		  AND r.subject_type = 'action' AND r.subject_id = ?
		ORDER BY a.n`, id)
	if err != nil {
		return nil, err
	}
	for _, chase := range chases {
		if err := closeRecord(ctx, t, chase, ClosedObsolete); err != nil {
			return nil, err
		}
	}
	return chases, nil
}

// freeDependents returns the actions that became ready because this one
// closed. An action with other open blockers stays blocked, which is why the
// state is recomputed rather than assigned.
func (t *Tx) freeDependents(ctx context.Context, id string) ([]*Action, error) {
	dependents, err := t.Dependents(ctx, id)
	if err != nil {
		return nil, err
	}

	var freed []*Action
	for _, d := range dependents {
		was := d.State
		if err := t.applyBlockedState(ctx, d); err != nil {
			return nil, err
		}
		if d.State != was {
			freed = append(freed, d)
		}
	}
	return freed, nil
}

// unhide brings back the actions that were folded out of the queue behind
// this one. What they were waiting on is gone, so the judgement that there
// was nothing to do about them has expired.
func (t *Tx) unhide(ctx context.Context, id string) ([]*Action, error) {
	hidden, err := t.Hiding(ctx, id)
	if err != nil {
		return nil, err
	}

	for _, h := range hidden {
		after := h.Clone()
		after.HiddenBehind = sql.NullString{}
		// The allowance starts now. It was not actionable while it was hidden,
		// and a step that surfaces already past its deadline is the thing the
		// hidden_behind exclusion exists to prevent — it would simply move the
		// same wrong report to the moment the one in front closed.
		after.ReadySince = sql.NullString{String: t.at, Valid: true}
		if _, err := t.Update(ctx, h, after); err != nil {
			return nil, err
		}
		*h = *after
	}
	return hidden, nil
}

func validClosedReason(reason string) bool {
	for _, known := range ClosedReasons {
		if reason == known {
			return true
		}
	}
	return false
}

// Terminal project statuses: a project in one of these is finished with, and
// closing it again is a mistake rather than a no-op.
func isTerminalStatus(status string) bool {
	switch status {
	case ProjectDone, ProjectRetired, ProjectSuperseded:
		return true
	}
	return false
}

// ProjectCloseResult is what closing a project did.
type ProjectCloseResult struct {
	Project *Project
	// Dropped are the open actions the closure abandoned. An action exists
	// to advance a project, and a closed project cannot be advanced.
	Dropped []*Action
	// Freed are the actions that became ready because a dropped one stopped
	// blocking them, and the ones that stopped being hidden behind it.
	Freed []*Action
	// Unblocked are the projects that became active because this one closed.
	// The project half of the same cascade, which did not exist until there
	// was a project-to-project edge to run it over.
	Unblocked []*Project
}

// CloseProject closes a project and drops whatever was still open on it.
//
// Leaving them is the bug this exists to fix: an action outliving its project
// sits in `action list --unblocked` pointing at work nobody wants, and the
// queue is only worth reading if everything in it is worth doing.
//
// status must be done or retired. Superseding is its own act, because it
// records where the work went, which this cannot know.
//
// Everything happens in one transaction and under one correlation id, so the
// log reads as a single decision rather than as a project closing and some
// unrelated actions being abandoned nearby.
func (s *Store) CloseProject(ctx context.Context, actor Actor, id, status string) (*ProjectCloseResult, error) {
	switch status {
	case ProjectDone, ProjectRetired:
	case ProjectSuperseded:
		return nil, fmt.Errorf("use `roz project supersede` for %s: it records where the work went",
			ProjectSuperseded)
	default:
		return nil, fmt.Errorf("%q does not close a project: use %s or %s",
			status, ProjectDone, ProjectRetired)
	}

	tx, err := s.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	before, err := tx.LoadProject(ctx, id)
	if err != nil {
		return nil, err
	}
	if isTerminalStatus(before.Status) {
		return nil, fmt.Errorf("%s is already %s", before.ID, before.Status)
	}

	after := before.Clone()
	after.Status = status
	// A snooze is about when to look again, and there is no again.
	after.SnoozeUntil = sql.NullString{}
	after.SnoozeReason = ""
	if _, err := tx.Update(ctx, before, after); err != nil {
		return nil, err
	}

	result := &ProjectCloseResult{Project: after}
	if result.Dropped, result.Freed, err = tx.dropOpenActions(ctx, after.ID); err != nil {
		return nil, err
	}

	// Projects waiting on this one are freed the way actions are. Without
	// this a project blocked on another stayed blocked for ever, since
	// nothing else ever re-evaluated the edge.
	if result.Unblocked, err = tx.freeBlockedProjects(ctx, after.ID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// dropOpenActions abandons every open action advancing a project, and
// returns what that freed.
//
// Each is dropped as obsolete rather than dropped: the distinction is why it
// stopped mattering, and "the project it advanced was closed" is not the same
// as "we decided against it". Their cascades still run, so an action blocked
// behind one of these is released rather than left waiting on something that
// will never move.
func (t *Tx) dropOpenActions(ctx context.Context, projectID string) (dropped, freed []*Action, err error) {
	open, err := t.loadActions(ctx,
		"SELECT %s FROM action a WHERE a.closed_at IS NULL AND a.project_id = ? ORDER BY a.n", projectID)
	if err != nil {
		return nil, nil, err
	}

	for _, a := range open {
		if err := closeRecord(ctx, t, a, ClosedObsolete); err != nil {
			return nil, nil, err
		}
		dropped = append(dropped, a)

		unblocked, err := t.freeDependents(ctx, a.ID)
		if err != nil {
			return nil, nil, err
		}
		unhidden, err := t.unhide(ctx, a.ID)
		if err != nil {
			return nil, nil, err
		}
		// A chase about an action being dropped with its project is as stale
		// as one about an action that finished.
		if _, err := t.standDownChases(ctx, a.ID); err != nil {
			return nil, nil, err
		}
		freed = append(freed, unblocked...)
		freed = append(freed, unhidden...)
	}

	// Something freed by one drop may be dropped by a later one, since they
	// can belong to the same project. Report only what survived.
	//
	// The check is against the dropped list rather than each record's own
	// state: a freed action was read before the later drop rewrote it, so the
	// copy in hand still says ready.
	return dropped, notDropped(freed, dropped), nil
}

func notDropped(freed, dropped []*Action) []*Action {
	gone := make(map[string]bool, len(dropped))
	for _, a := range dropped {
		gone[a.ID] = true
	}

	var survived []*Action
	for _, a := range freed {
		if !gone[a.ID] {
			survived = append(survived, a)
		}
	}
	return survived
}
