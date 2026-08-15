package main

import (
	"fmt"

	"github.com/scottlaird/roz/internal/filter"
	"github.com/scottlaird/roz/internal/store"
)

func main() {
	fmt.Println("joins on Project:", len(store.Joins(&store.Project{})))
	for name, j := range store.Joins(&store.Project{}) {
		fmt.Printf("  %-10s table=%-14s near=%-10s far=%-11s kind=%v via=%v\n",
			name, j.Table, j.Near, j.Far, j.Kind, j.Via != nil)
	}
	table, err := store.TableOf(&store.Project{})
	fmt.Println("TableOf:", table, err)

	for _, expr := range []string{
		`actions.exists(a, a.verb == "wait_ref")`,
		`issues.size() == 0`,
		`children.exists(c, c.priority < project.priority)`,
	} {
		f, err := filter.Compile(&store.Project{}, expr)
		if err != nil {
			fmt.Printf("%-46s ERROR %v\n", expr, err)
			continue
		}
		where, args := f.SQL()
		fmt.Printf("%-46s → %s  %v\n", expr, where, args)
	}
}
