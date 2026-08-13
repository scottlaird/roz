package store

import (
	"context"
	"fmt"
	"strings"
)

// TreeNode is one project and how deep it sits.
type TreeNode struct {
	Project *Project
	// Depth is 0 for a project with no parent among those given.
	Depth int
	// Context marks a project included only to connect the ones that were
	// asked for — a closed parent above open work. It is shown, because
	// hiding it would orphan its children, and marked, because it is not
	// itself something to do.
	Context bool
}

// Tree arranges projects into parents and children, keeping the order it was
// given within each level.
//
// The order is the caller's: whatever `--sort` chose applies at every depth
// rather than being replaced by the shape. A hierarchy is a way of reading a
// list, not a different ranking of it.
//
// A project whose parent is not among those given is a root. That matters
// because the list is usually filtered — showing only open projects would
// otherwise drop a child whose parent is closed, which is the opposite of what
// filtering asked for.
//
// Anything left over after walking down from the roots is appended flat. It
// can only happen where a cycle exists, which nothing here can write; losing
// those rows silently would be worse than drawing them oddly.
func Tree(projects []*Project) []TreeNode {
	present := make(map[string]bool, len(projects))
	for _, p := range projects {
		present[p.ID] = true
	}

	children := map[string][]*Project{}
	var roots []*Project
	for _, p := range projects {
		parent := p.ParentID.String
		if p.ParentID.Valid && parent != "" && present[parent] {
			children[parent] = append(children[parent], p)
			continue
		}
		roots = append(roots, p)
	}

	nodes := make([]TreeNode, 0, len(projects))
	placed := make(map[string]bool, len(projects))

	var walk func(p *Project, depth int)
	walk = func(p *Project, depth int) {
		if placed[p.ID] {
			return
		}
		placed[p.ID] = true
		nodes = append(nodes, TreeNode{Project: p, Depth: depth, Context: p.contextOnly})
		for _, child := range children[p.ID] {
			walk(child, depth+1)
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}

	for _, p := range projects {
		if !placed[p.ID] {
			nodes = append(nodes, TreeNode{Project: p, Context: p.contextOnly})
		}
	}
	return nodes
}

// WithAncestors returns the given projects plus any parent missing from the
// set, so a tree drawn over them has no gaps.
//
// A closed project above open work is still worth drawing: it says what the
// open work is part of, which is the whole point of the hierarchy. It is
// marked as context rather than listed as though it were live.
//
// Filtered out by having no open descendants, which is the same thing as not
// being reached from here — a closed project whose children are all closed too
// is not an ancestor of anything in the set, so nothing pulls it in. That is
// why this needs no recursive count of open children: the set being connected
// already answers it.
func (s *Store) WithAncestors(ctx context.Context, projects []*Project) ([]*Project, error) {
	have := make(map[string]bool, len(projects))
	for _, p := range projects {
		have[p.ID] = true
	}

	out := append([]*Project(nil), projects...)
	// Breadth-first up the chains, so a grandparent arrives through its child
	// rather than being looked for twice.
	frontier := projects
	for len(frontier) > 0 {
		var wanted []string
		for _, p := range frontier {
			if p.ParentID.Valid && p.ParentID.String != "" && !have[p.ParentID.String] {
				have[p.ParentID.String] = true
				wanted = append(wanted, p.ParentID.String)
			}
		}
		if len(wanted) == 0 {
			break
		}
		found, err := s.projectsByID(ctx, wanted)
		if err != nil {
			return nil, err
		}
		for _, p := range found {
			p.contextOnly = true
		}
		out = append(out, found...)
		frontier = found
	}
	return out, nil
}

// projectsByID reads a set of projects in one query.
func (s *Store) projectsByID(ctx context.Context, ids []string) ([]*Project, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	fields, err := fieldsOfStruct(&Project{})
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.column
	}

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := fmt.Sprintf("SELECT %s FROM project WHERE id IN (%s) ORDER BY n",
		strings.Join(columns, ", "), strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading projects: %w", err)
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		var p Project
		dest := make([]any, len(fields))
		for i, f := range fields {
			dest[i] = f.pointerOf(&p)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("reading projects: %w", err)
		}
		projects = append(projects, &p)
	}
	return projects, rows.Err()
}
