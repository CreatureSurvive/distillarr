package jobs

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/scan"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// RetryConfirmHardlinked must mark the job's settings as confirmed and
// return it to queued, so the next run's pre-replace check passes.
func TestRetryConfirmHardlinked(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.NewManager(st)
	sc := scan.New(st, cfg)
	e := New(st, cfg, sc)

	settings := encode.Settings{Codec: encode.HEVC, Backend: encode.SW, Quality: 50}
	sj, _ := json.Marshal(settings)
	j := &store.Job{SrcPath: "/m/x.mkv", Backend: "sw", Codec: "hevc", Quality: 50,
		SettingsJSON: string(sj), MaxAttempts: 3}
	if err := st.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	// Fail it the way the engine does for a hardlinked-during-encode job,
	// so it's eligible for retry.
	if err := st.FailJobKeepAttempt(j.ID, "hardlinked: file now shares its data with another link (nlink=2); confirm to replace anyway", ""); err != nil {
		t.Fatal(err)
	}

	if err := e.RetryConfirmHardlinked(j.ID); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetJob(j.ID)
	if err != nil || got == nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != store.StatusQueued {
		t.Errorf("status = %q, want queued", got.Status)
	}
	var gotSettings encode.Settings
	if err := json.Unmarshal([]byte(got.SettingsJSON), &gotSettings); err != nil {
		t.Fatal(err)
	}
	if !gotSettings.ConfirmedHardlinked {
		t.Error("settings.confirmed_hardlinked was not set")
	}
	// Everything else about the settings must be preserved.
	if gotSettings.Codec != encode.HEVC || gotSettings.Backend != encode.SW || gotSettings.Quality != 50 {
		t.Errorf("settings changed unexpectedly: %+v", gotSettings)
	}
}

// A job with no prior settings_json (shouldn't happen in practice, but
// must not panic) still gets the flag and requeues cleanly.
func TestRetryConfirmHardlinkedEmptySettings(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.NewManager(st)
	sc := scan.New(st, cfg)
	e := New(st, cfg, sc)

	j := &store.Job{SrcPath: "/m/y.mkv", Backend: "sw", Codec: "hevc", MaxAttempts: 3}
	if err := st.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := st.FailJobKeepAttempt(j.ID, "hardlinked: …", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.RetryConfirmHardlinked(j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetJob(j.ID)
	var s encode.Settings
	if err := json.Unmarshal([]byte(got.SettingsJSON), &s); err != nil {
		t.Fatal(err)
	}
	if !s.ConfirmedHardlinked {
		t.Error("confirmed_hardlinked was not set")
	}
}
