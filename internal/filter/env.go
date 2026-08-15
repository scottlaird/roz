package filter

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/spandigital/cel2sql/v3"
	"github.com/spandigital/cel2sql/v3/dialect"

	// The SQLite dialect registers itself, and is the only one roz can use.
	_ "github.com/spandigital/cel2sql/v3/dialect/sqlite"
	"github.com/spandigital/cel2sql/v3/schema"

	"github.com/scottlaird/roz/internal/store"
)

// tableName is what the schema is registered under.
//
// Arbitrary, and never seen: the converter wants a table to hang a schema off,
// while roz's filters are always over the one entity the listing is of, so
// there is nothing to disambiguate and no reason to make anybody type it.
const tableName = "row"

// envFor declares one variable per column, so a filter can be written in the
// same names --fields and --sort already accept.
//
// Every column is Dyn rather than its concrete type, which is a real trade.
// Dyn is what lets `merged_at != null` compile at all — CEL has no nullable
// string, and a column that is NULL half the time is most of what anybody
// wants to filter on. The cost is that `number > "x"` gets past the type
// checker and fails at evaluation instead, which for an interactive listing is
// an error either way.
func envFor(columns []store.ColumnType, joins *joinEnv) (*cel.Env, error) {
	opts := make([]cel.EnvOption, 0, len(columns)+4)
	for _, c := range columns {
		opts = append(opts, cel.Variable(c.Name, cel.DynType))
	}
	if joins != nil {
		opts = append(opts, joins.declare()...)
	}
	// Macro tracking keeps the original `rel.exists(v, pred)` call beside the
	// comprehension it expands into. Recognising the traversal from the
	// expansion would mean matching cel-go's accumulator-and-loop-step shape,
	// which is its implementation rather than the language.
	opts = append(opts, cel.EnableMacroCallTracking())

	env, err := cel.NewEnv(opts...)
	if err != nil {
		return nil, fmt.Errorf("building the filter vocabulary: %w", err)
	}
	return env, nil
}

// schemasFor describes the base row and every relation to the converter, each
// under the name a filter uses for it.
func schemasFor(columns []store.ColumnType, joins *joinEnv, iter, relation string) map[string]schema.Schema {
	schemas := map[string]schema.Schema{tableName: schemaOf(columns)}
	if joins == nil {
		return schemas
	}
	// The outer row, under its table name: inside a subquery a bare column
	// binds to the inner table, so an outer reference has to be qualified.
	schemas[joins.base] = schemaOf(columns)
	if iter != "" {
		schemas[iter] = schemaOf(joins.columns[relation])
	}
	for name := range joins.joins {
		if far, ok := joins.columns[name]; ok && name != iter {
			schemas[name] = schemaOf(far)
		}
	}
	return schemas
}

// toSQL converts one term, or says it cannot.
//
// Parameterised rather than inlined: the values in a filter are somebody's
// typing, and a WHERE clause built by string concatenation from typing is the
// oldest mistake there is.
func toSQL(ast *cel.Ast, schemas map[string]schema.Schema) (string, []any, error) {
	sqlite, err := dialect.Get(dialect.SQLite)
	if err != nil {
		return "", nil, err
	}
	result, err := cel2sql.ConvertParameterized(ast,
		cel2sql.WithDialect(sqlite),
		cel2sql.WithSchemas(schemas))
	if err != nil {
		return "", nil, err
	}
	return result.SQL, result.Parameters, nil
}

// schemaOf describes the columns to the converter, which needs the types to
// decide what SQL a comparison becomes — a JSON column is a json_each
// subquery, a scalar is a comparison.
func schemaOf(columns []store.ColumnType) schema.Schema {
	fields := make([]schema.FieldSchema, 0, len(columns))
	for _, c := range columns {
		field := schema.FieldSchema{Name: c.Name, Type: c.Kind}
		if c.JSON {
			// roz's JSON columns are all arrays of strings: the reviewer
			// teams, the approvals, the required owners.
			field.Type = "text"
			field.IsJSON = true
			field.Repeated = true
			field.Dimensions = 1
			field.ElementType = "text"
		}
		fields = append(fields, field)
	}
	return schema.NewSchema(fields)
}

// nullFilled supplies a null for every column the record had no value for.
//
// Without it, evaluating `state != "OPEN"` against a row whose state is NULL
// fails with "no such attribute" rather than answering — and the whole point
// of the Go pass is that it answers the same question SQL was asked.
func nullFilled(values map[string]any, columns []string) map[string]any {
	filled := make(map[string]any, len(columns))
	for _, name := range columns {
		if value, ok := values[name]; ok {
			filled[name] = value
			continue
		}
		filled[name] = types.NullValue
	}
	return filled
}
