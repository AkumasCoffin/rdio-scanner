package main

import (
	"testing"
	"time"
)

// The live path is fromMap, not a struct built in Go — and that is the path the
// cursor was reported broken on. Ties matter most: if the id tiebreak is lost, a
// boundary landing inside a group of identical timestamps repeats rows.
func TestCursorThroughTheWirePathExcludesTheCursorRow(t *testing.T) {
	db := newTestDatabase(t)
	defer db.Sql.Close()

	base := time.Date(2024, 3, 5, 12, 0, 0, 0, time.UTC)

	ids := []uint{}
	for n := 0; n < 6; n++ {
		ids = append(ids, insertSearchTestCall(t, db, base, 1, uint(100+n)))
	}

	client := searchTestClient(db)
	calls := NewCalls()

	middle := ids[3]
	options := &CallsSearchOptions{}
	if err := options.fromMap(map[string]any{
		"limit":  float64(10),
		"sort":   float64(-1),
		"cursor": true,
		"after": map[string]any{
			"dateTime": base.Format(time.RFC3339),
			"id":       float64(middle),
		},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := calls.Search(options, client)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results.Results {
		if r.Id == middle {
			t.Errorf("the cursor row %d came back in its own continuation", middle)
		}
		if r.Id > middle {
			t.Errorf("descending cursor returned id %d, above the cursor %d — the id tiebreak is not applied", r.Id, middle)
		}
	}

	t.Logf("cursor %d returned %d rows", middle, len(results.Results))
}
