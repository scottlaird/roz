package store

import "context"

// A Relation is one thing an entity is connected to that is not a column of
// its own table.
//
// The declaration names the connection and the function that reads it. It
// deliberately does not describe the join: the queries stay hand-written and
// tested where they are, and generating them was never the point. What this
// buys is that a reader of the entity can see what it is connected to, and
// that the things which print those connections stop each deciding for
// themselves what an action is attached to.
//
// A connection that is already a column needs nothing here. `action.project_id`
// and `action.hidden_behind` are declared by their `db` tags like anything
// else; only what lives in another table is missing, which is why this list
// is short.
type Relation struct {
	// Name is the key in JSON and the label in a detail listing.
	Name string
	// Load reads the far end for one record. Returning nil means there is
	// nothing there, and the relation is left out entirely rather than
	// rendered empty.
	Load func(ctx context.Context, tx *Tx, id string) (any, error)
}

// Related is a Record connected to something beyond its own columns.
//
// The method is unexported for the same reason Record's are: what an entity
// is attached to is part of its definition, not something to be extended from
// outside.
type Related interface {
	Record
	relations() []Relation
}

// Relations loads everything a record is connected to, keyed by name.
//
// It returns nil for a record that declares none, so a caller can treat "has
// no relations" and "has none loaded" the same way — which is what lets the
// JSON encoder take any record at all.
//
// One query per relation, which is right for showing one record and wrong for
// a list. The status page keeps its own batched loaders for exactly that
// reason; see PRsByAction and JiraByProject.
func (t *Tx) Relations(ctx context.Context, r Record) (map[string]any, error) {
	related, ok := r.(Related)
	if !ok {
		return nil, nil
	}

	var loaded map[string]any
	for _, rel := range related.relations() {
		value, err := rel.Load(ctx, t, r.subjectID())
		if err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}
		if loaded == nil {
			loaded = map[string]any{}
		}
		loaded[rel.Name] = value
	}
	return loaded, nil
}

// MarshalRecord encodes a record and what it is connected to.
//
// The package-level MarshalRecord takes no transaction and so can only see
// columns. That is the right answer for a list, where a query per row per
// relation would be the wrong shape; it was the wrong answer for `show`,
// where the table printed an action's blockers and the JSON silently did not.
func (t *Tx) MarshalRecord(ctx context.Context, r Record) ([]byte, error) {
	loaded, err := t.Relations(ctx, r)
	if err != nil {
		return nil, err
	}
	return marshalRecordWith(r, loaded)
}

// relations for an action: both directions of the blocking edge, and the
// pull requests it is about.
//
// `blocking` is the direction the cascade already reads when it decides what
// closing this frees, and the ranking counts. It was visible nowhere.
func (a *Action) relations() []Relation {
	return []Relation{
		{Name: "blocked_by", Load: blockedByIDs},
		{Name: "blocking", Load: blockingIDs},
		{Name: "subject_pr", Load: subjectPRID},
		{Name: "context_prs", Load: contextPRIDs},
	}
}

// relations for a project: the Jira issues it tracks.
//
// Its actions are the other direction of action.project_id, which is a column
// on the far side and reachable with `action list --project`. Repeating it
// here would put the same fact in two places.
func (p *Project) relations() []Relation {
	return []Relation{
		{Name: "jira", Load: jiraKeys},
	}
}

// relations for a pull request: the actions about it.
//
// This is the direction that was hardest to get at — a tracked pull request
// gave no clue which piece of work it belonged to.
func (r *PR) relations() []Relation {
	return []Relation{
		{Name: "actions", Load: actionsAboutPR},
	}
}

func blockedByIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	blockers, err := tx.OpenBlockers(ctx, id)
	if err != nil || len(blockers) == 0 {
		return nil, err
	}
	return blockers, nil
}

func blockingIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	dependents, err := tx.Dependents(ctx, id)
	if err != nil || len(dependents) == 0 {
		return nil, err
	}
	return actionIDs(dependents), nil
}

// The subject and the context links are two relations rather than one list
// with a role on each entry, because they are two different questions. The
// schema allows at most one subject and closing reads only that; a context
// link is background and is never asked anything. Declaring them separately
// puts that rule in the shape instead of in a comment, and both render as
// themselves rather than as a blob of JSON.
func subjectPRID(ctx context.Context, tx *Tx, id string) (any, error) {
	pr, ok, err := tx.SubjectPR(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return pr, nil
}

func contextPRIDs(ctx context.Context, tx *Tx, id string) (any, error) {
	links, err := tx.PRsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, link := range links {
		if link.Role == RoleContext {
			ids = append(ids, link.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, nil
}

func jiraKeys(ctx context.Context, tx *Tx, id string) (any, error) {
	keys, err := tx.JiraKeysForProject(ctx, id)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	return keys, nil
}

func actionsAboutPR(ctx context.Context, tx *Tx, id string) (any, error) {
	actions, err := tx.LinkedActions(ctx, id)
	if err != nil || len(actions) == 0 {
		return nil, err
	}
	return actionIDs(actions), nil
}

func actionIDs(actions []*Action) []string {
	ids := make([]string, len(actions))
	for i, a := range actions {
		ids[i] = a.ID
	}
	return ids
}
