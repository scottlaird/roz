package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/scottlaird/roz/internal/store"
	"github.com/spf13/cobra"
)

// record is what the read-modify-write needs of a row: the store's contract,
// and a copy to apply the change to so Update can see what moved.
type record[T any] interface {
	store.Record
	Clone() T
}

// loader reads one row by identifier inside a transaction. Every Load* method
// on Tx has this shape, so the method expression is passed as is.
type loader[T any] func(*store.Tx, context.Context, string) (T, error)

// updateRecord is the read-modify-write every CLI verb performs: open the
// store as the actor the flags name, load, apply the change to a clone, let
// Tx.Update work out what moved, and say so.
//
// One body rather than one per entity, because the five it replaced were the
// same forty lines modulo the type, and a decision about how "unchanged" is
// reported — or how the actor is chosen — needed five edits to keep true.
//
// Nothing is written when the change is a no-op, and the actor rule is
// applied by Update rather than here.
func updateRecord[T record[T]](cmd *cobra.Command, id string, load loader[T],
	change func(context.Context, *store.Tx, T) error) ([]store.Change, error) {

	actor, err := actorFrom(cmd)
	if err != nil {
		return nil, err
	}
	st, err := openStore(cmd)
	if err != nil {
		return nil, err
	}
	defer st.Close()

	return updateIn(cmd.Context(), cmd.OutOrStdout(), st, actor, id, load, change)
}

// updateIn is updateRecord from the store inward, for a caller that already
// holds one — or an actor that no flag names.
func updateIn[T record[T]](ctx context.Context, out io.Writer, st *store.Store, actor store.Actor,
	id string, load loader[T], change func(context.Context, *store.Tx, T) error) ([]store.Change, error) {

	tx, err := st.Begin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	before, err := load(tx, ctx, id)
	if err != nil {
		return nil, notFoundOr(err, id)
	}

	after := before.Clone()
	if err := change(ctx, tx, after); err != nil {
		return nil, err
	}

	changes, err := tx.Update(ctx, before, after)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if len(changes) == 0 {
		fmt.Fprintf(out, "%s unchanged\n", id)
		return nil, nil
	}
	for _, change := range changes {
		fmt.Fprintf(out, "%s %s\n", id, change)
	}
	return changes, nil
}
