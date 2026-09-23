package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestJoinTagIDsAndTagIDsRoundTrip(t *testing.T) {
	for _, ids := range [][]int64{nil, {}, {3}, {3, 7}, {7, 3, 12}} {
		joined := JoinTagIDs(ids)
		a := ArrItem{Tags: joined}
		got := a.TagIDs()
		if len(ids) == 0 {
			if len(got) != 0 {
				t.Errorf("TagIDs(JoinTagIDs(%v)) = %v, want empty", ids, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, ids) {
			t.Errorf("TagIDs(JoinTagIDs(%v)) = %v", ids, got)
		}
	}
	if JoinTagIDs([]int64{3, 7}) != ",3,7," {
		t.Errorf("JoinTagIDs format = %q, want ,3,7,", JoinTagIDs([]int64{3, 7}))
	}
}

// Regression test for a real bug: chunking a "NOT IN (subset)" delete is
// wrong when keep has more entries than one chunk — each chunk only sees
// its own slice, so it deletes every row not in THAT chunk, including
// rows a different chunk was supposed to keep. On a real host with 9000+
// Sonarr episode files this deleted nearly everything UpsertArrItems had
// just inserted, in the same sync pass.
func TestDeleteArrItemsForInstanceExceptSurvivesMoreThanOneChunk(t *testing.T) {
	st, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const n = 1200 // more than two 500-row chunks
	items := make([]ArrItem, n)
	keep := make([]int64, n)
	for i := 0; i < n; i++ {
		f := &File{Path: fmt.Sprintf("/m/%d.mkv", i), Library: "movies", Title: fmt.Sprintf("T%d", i)}
		if err := st.UpsertFile(f, nil); err != nil {
			t.Fatal(err)
		}
		items[i] = ArrItem{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: int64(i), FileRecID: int64(i)}
		keep[i] = f.ID
	}
	if err := st.UpsertArrItems(items); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteArrItemsForInstanceExcept("radarr", keep); err != nil {
		t.Fatal(err)
	}

	got, err := st.ArrItemsMap(keep)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("kept %d of %d items across chunk boundaries, want all %d", len(got), n, n)
	}
}

func TestDeleteArrItemsForInstanceExceptRemovesOnlyStale(t *testing.T) {
	st, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := &File{Path: "/m/a.mkv", Library: "movies", Title: "A"}
	b := &File{Path: "/m/b.mkv", Library: "movies", Title: "B"}
	if err := st.UpsertFile(a, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(b, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertArrItems([]ArrItem{
		{FileID: a.ID, InstanceID: "radarr", Kind: "radarr"},
		{FileID: b.ID, InstanceID: "radarr", Kind: "radarr"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteArrItemsForInstanceExcept("radarr", []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ArrItemByFileID(a.ID); got == nil {
		t.Error("a should survive (kept)")
	}
	if got, _ := st.ArrItemByFileID(b.ID); got != nil {
		t.Error("b should be removed (not in keep)")
	}
}

func TestArrTagsCacheRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if got := st.ArrTags("sonarr"); len(got) != 0 {
		t.Errorf("uncached instance = %v, want empty", got)
	}
	if err := st.SetArrTags("sonarr", map[int64]string{3: "keep-quality", 7: "anime"}); err != nil {
		t.Fatal(err)
	}
	got := st.ArrTags("sonarr")
	if got[3] != "keep-quality" || got[7] != "anime" {
		t.Errorf("got %v", got)
	}
	// A different instance's cache must stay independent.
	if got := st.ArrTags("radarr"); len(got) != 0 {
		t.Errorf("radarr cache = %v, want empty (only sonarr was set)", got)
	}
}
