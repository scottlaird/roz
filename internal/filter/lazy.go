package filter

import (
	"context"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/interpreter"

	"github.com/scottlaird/roz/internal/store"
)

// A Loader reads the far side of a relation for one record.
//
// The Go pass needs it for any traversal SQL would not take. It exists as an
// interface so the filter package does not hold a database: the caller has one
// open already, and a filter that opened its own would be a second connection
// with its own view of the data.
type Loader interface {
	Related(ctx context.Context, join store.Join, id string) ([]map[string]any, error)
}

// WithLoader gives a filter somewhere to read relations from.
//
// Without one, a residual that traverses cannot be evaluated at all — and
// says so rather than answering. That distinction is the whole reason this is
// not optional-and-silent: a filter that quietly matched nothing because
// nobody wired a loader is indistinguishable from a filter that correctly
// matched nothing.
func (f *Filter) WithLoader(ctx context.Context, loader Loader) *Filter {
	if f == nil {
		return nil
	}
	f.ctx, f.loader = ctx, loader
	return f
}

// rowActivation resolves a filter's variables for one record, reading
// relations only if the expression asks for them.
//
// Lazy per name, and cached per row. An expression that never mentions
// `actions` costs no query; one that mentions it twice costs one. This is what
// keeps the Go pass from loading the database to answer a question about a
// column — and it is still a query per row per relation, which is the cost
// that argues for pushing the traversal into SQL instead.
type rowActivation struct {
	ctx    context.Context
	values map[string]any
	joins  *joinEnv
	loader Loader
	id     string
	// mentions are the names the expression selects, which is how a far row
	// knows which of its own relations are worth reading.
	mentions map[string]bool

	// loaded caches what has already been read for this row.
	loaded map[string]any
}

func (a *rowActivation) Parent() interpreter.Activation { return nil }

func (a *rowActivation) ResolveName(name string) (any, bool) {
	if value, ok := a.values[name]; ok {
		return value, true
	}
	if value, ok := a.loaded[name]; ok {
		return value, true
	}

	// The outer row, under its own table name: the same thing the columns
	// hold, addressed the way a traversal has to address it.
	if a.joins != nil && name == a.joins.base {
		return a.values, true
	}

	join, ok := a.relation(name)
	if !ok {
		return nil, false
	}
	if a.loader == nil {
		// Nothing to read with. Reporting absence would answer the question
		// wrongly, so this resolves to nothing at all and evaluation fails
		// with a name error the caller turns into a message.
		return nil, false
	}

	rows, err := a.loader.Related(a.ctx, join, a.id)
	if err != nil {
		return nil, false
	}
	// A far row that the expression reaches through again needs its own
	// relations, or the second hop is a missing key — which CEL calls an
	// error and Keep reads as "does not match", for every row. See follow.
	a.follow(join, rows, 1)
	value := valueOfRelation(join, rows)
	if a.loaded == nil {
		a.loaded = map[string]any{}
	}
	a.loaded[name] = value
	return value, true
}

func (a *rowActivation) relation(name string) (store.Join, bool) {
	if a.joins == nil {
		return store.Join{}, false
	}
	join, ok := a.joins.joins[name]
	return join, ok
}

// valueOfRelation shapes what was read the way the expression expects it: a
// list for a to-many, and the record or null for a to-one.
func valueOfRelation(join store.Join, rows []map[string]any) any {
	if join.Kind == store.ToOne {
		if len(rows) == 0 {
			return types.NullValue
		}
		return rows[0]
	}
	list := make([]any, 0, len(rows))
	for _, row := range rows {
		list = append(list, row)
	}
	return list
}

// follow loads the relations of a far row that the expression names, so a
// chain can be evaluated here rather than only in the query.
//
// Only the names the expression mentions, which is what keeps this from
// reading the database to answer a question about a column: `actions.exists(a,
// a.verb == "merge")` follows nothing, and `actions.exists(a,
// a.project.priority == 1)` follows `project` alone.
//
// It is a query per far row per named relation, so this is N×M where the
// pushed-down form is one statement. That is the honest cost of answering in
// Go at all, and the reason --explain-filter says which happened: a listing
// that pushes the whole chain down pays none of it.
//
// Bounded by maxHops, for the same reason the generator is: each level
// multiplies, and the person waiting is the person who wrote the expression.
func (a *rowActivation) follow(join store.Join, rows []map[string]any, depth int) {
	if depth > maxHops || a.loader == nil || join.Blank == nil || len(a.mentions) == 0 {
		return
	}
	far, err := newJoinEnv(join.Blank())
	if err != nil || far == nil {
		return
	}

	for _, row := range rows {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		for name, next := range far.joins {
			if !a.mentions[name] {
				continue
			}
			if _, already := row[name]; already {
				// A column of the far row that happens to share a relation's
				// name. The column is the answer: it is what the row actually
				// holds.
				continue
			}
			beyond, err := a.loader.Related(a.ctx, next, id)
			if err != nil {
				continue
			}
			a.follow(next, beyond, depth+1)
			row[name] = valueOfRelation(next, beyond)
		}
	}
}
