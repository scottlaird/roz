package filter

import (
	"database/sql"
	"testing"
)

// TestReadingTheSQLCommitsToNothing pins the separation of read from
// commitment. #200 called Explain before the query ran, and Explain read the
// fragment; reading it used to mark the filter as pushed down, so every
// listing then evaluated only the residual and let through rows the query
// had been trusted to remove.
func TestReadingTheSQLCommitsToNothing(t *testing.T) {
	f, err := Compile(&row{}, `state == "CLOSED"`)
	if err != nil {
		t.Fatalf("Compile() returned error: %v", err)
	}
	if where, _ := f.SQL(); where == "" {
		t.Fatalf("nothing pushed down, so the test proves nothing")
	}
	open := &row{State: sql.NullString{String: "OPEN", Valid: true}}

	// Read, not taken: Keep still runs the whole expression in Go.
	if ok, err := f.Keep(open); err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	} else if ok {
		t.Errorf("after only reading the SQL, Keep passed a row the expression rejects")
	}

	// Taken: the query did its half, so Keep answers true for everything.
	f.Take()
	if ok, err := f.Keep(open); err != nil {
		t.Fatalf("Keep() returned error: %v", err)
	} else if !ok {
		t.Errorf("after taking the SQL, Keep re-ran what the query already answered")
	}
}
