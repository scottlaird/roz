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
	verb  string
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
				return "", fmt.Errorf("%s is not tracked; `todo pr track` it first", linking)
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
	r, err := tx.LoadGitHubRepo(ctx, repo)
	if err != nil {
		return err
	}
	if !r.Pipeline.Valid {
		return nil
	}

	pipeline, err := tx.LoadPipeline(ctx, r.Pipeline.String)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s names pipeline %q, which does not exist",
				repo, r.Pipeline.String)
		}
		return err
	}
	p.pipeline = pipeline.Name

	pr, err := tx.LoadPR(ctx, p.subject)
	if err != nil {
		return err
	}

	for _, step := range pipeline.Steps {
		verb, err := tx.LoadVerb(ctx, step)
		if err != nil {
			return fmt.Errorf("reading step %q of pipeline %s: %w", step, pipeline.Name, err)
		}
		if satisfied(verb, pr) {
			p.skipped = append(p.skipped, step)
			continue
		}
		p.steps = append(p.steps, plannedStep{
			verb:  step,
			title: fmt.Sprintf("%s %s", verb.Label, p.subject),
		})
	}
	return nil
}

// satisfied reports whether a step is already true of the pull request, and
// so has nothing to do.
func satisfied(verb *ActionVerb, pr *PR) bool {
	predicate, ok := verb.Predicate()
	return ok && predicate(pr)
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

	result.Created, err = tx.instantiate(ctx, steps, plan.subject)
	if err != nil {
		return nil, err
	}
	if result.Unblocked, err = tx.freeDependents(ctx, a.ID); err != nil {
		return nil, err
	}
	if result.Unhidden, err = tx.unhide(ctx, a.ID); err != nil {
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
	after.State = ActionDone
	if reason == ClosedDropped {
		after.State = ActionDropped
	}
	after.ClosedAt = sql.NullString{String: tx.at, Valid: true}
	after.ClosedReason = sql.NullString{String: reason, Valid: true}

	if _, err := tx.Update(ctx, a, after); err != nil {
		return err
	}
	*a = *after
	return nil
}

// instantiate writes the pipeline's actions, each blocked by the one before
// it, and each about the same pull request.
//
// Chaining them at creation is what makes the rest mechanical: closing a step
// frees the next through the same unblocking every other action gets, so
// there is no separate notion of "advancing a pipeline" to keep correct.
func (t *Tx) instantiate(ctx context.Context, steps []*Action, subject string) ([]*Action, error) {
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
	}
	return steps, nil
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
