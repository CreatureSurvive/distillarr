// mediatrans server: media analysis, transcoding & re-encoding.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mediatrans/internal/api"
	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/jellyfin"
	"mediatrans/internal/jobs"
	"mediatrans/internal/preview"
	"mediatrans/internal/recs"
	"mediatrans/internal/scan"
	"mediatrans/internal/still"
	"mediatrans/internal/store"
	"mediatrans/internal/tune"
)

//go:embed all:web/dist
var webDist embed.FS

func webFS() fs.FS {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		return nil
	}
	return sub
}

func main() {
	listen := envOr("MEDIIATRANS_LISTEN", ":8080")
	dbPath := envOr("MEDIIATRANS_DB", "/config/mediatrans.db")
	prevRoot := envOr("MEDIIATRANS_PREVIEWS", "/config/previews")

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("config dir: %v", err)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer st.Close()

	cfg := config.NewManager(st)
	sc := scan.New(st, cfg)
	eng := jobs.New(st, cfg, sc)

	// Recommendations resolve "auto" against probed hardware, learn from
	// real results, and use Jellyfin genres (animation) when synced.
	recs.InitCalibration(st)
	recs.ResolveBackend = func(pref string, c encode.Codec) encode.Backend { return eng.ResolveFor(pref, c) }
	recs.Genres = st.GenresFor

	// Preview notify is bound to the hub once the API server exists.
	var hubNotify func(string, any)
	pv := preview.NewManager(prevRoot, eng.AcquireSem,
		func(event string, data any) {
			if hubNotify != nil {
				hubNotify(event, data)
			}
		})
	stl := still.New(filepath.Join(prevRoot, "still"), eng.AcquireSem)
	srv := api.NewServer(st, cfg, sc, eng, pv, stl, webFS())
	hubNotify = srv.Hub().Broadcast
	pv.OnTuned = func(fileID int64, s encode.Settings, r tune.Result) {
		f, err := st.GetFile(fileID)
		if err != nil || f == nil {
			return
		}
		b, _ := json.Marshal(recs.TuneRecord{Target: s.VMAFTarget, Codec: string(s.Codec), Backend: string(s.Backend),
			Crop: s.Crop, MaxHeight: s.MaxHeight, Quality: r.Quality, Ratio: r.Ratio, VMAF: r.VMAF,
			Met: r.Met, At: time.Now().UTC().Format(time.RFC3339)})
		_ = st.SetTune(fileID, string(b))
		if r.Ratio > 0 {
			recs.RecordObservation(f, s, r.Ratio)
		}
		sc.RefreshRecsSoon()
	}
	pv.OnMeasured = func(fileID int64, s encode.Settings, ratio float64) {
		if s.UpscaleTo > 0 {
			return // an upscale's output size says nothing about re-encode savings
		}
		if f, err := st.GetFile(fileID); err == nil && f != nil {
			recs.RecordObservation(f, s, ratio)
			sc.RefreshRecsSoon()
		}
	}

	// Jellyfin post-job subscriber: refresh the item and restore its
	// DateCreated from the original file's birth time (Linux can't
	// rewrite crtime, so this keeps Jellyfin's "date added" intact). A
	// copy-mode upscale is a new file instead: tell Jellyfin where to look.
	eng.OnFinished(func(ev jobs.ReplacedEvent) {
		c := cfg.Get()
		if c.JellyfinURL == "" || c.JellyfinAPIKey == "" {
			return
		}
		cl := jellyfin.New(c.JellyfinURL, c.JellyfinAPIKey)
		if ev.Kind == "upscale-copy" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := cl.MediaUpdated(ctx, c.ToJellyfinPath(ev.NewPath), "Created"); err != nil {
				log.Printf("jellyfin: announce %s: %v", ev.NewPath, err)
			}
			return
		}
		row, err := st.JellyfinByPath(ev.NewPath)
		if err != nil || row == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := cl.Refresh(ctx, row.ItemID); err != nil {
			log.Printf("jellyfin refresh %s: %v", ev.NewPath, err)
		}
		if ev.OldStat != nil && ev.OldStat.BtimeSec > 0 {
			bt := time.Unix(ev.OldStat.BtimeSec, ev.OldStat.BtimeNsec)
			if err := cl.PatchDateCreated(ctx, row.ItemID, bt); err != nil {
				log.Printf("jellyfin DateCreated patch %s: %v", ev.NewPath, err)
			}
		}
	})

	eng.Start()
	defer eng.Stop()
	cropStop := make(chan struct{})
	defer close(cropStop)
	go sc.CropLoop(cropStop)
	// Recommendations depend on settings + hardware + calibration;
	// recompute once at boot so cached ones never go stale.
	go func() {
		time.Sleep(8 * time.Second)
		sc.RefreshRecs()
	}()
	// One-time reconciliation for jobs restored from trash before this
	// was tracked directly; a no-op on every later boot.
	go func() {
		time.Sleep(5 * time.Second)
		if n, err := st.BackfillRevertedJobs(); err != nil {
			log.Printf("history: backfill reverted jobs: %v", err)
		} else if n > 0 {
			log.Printf("history: marked %d already-reverted job(s) excluded from savings", n)
		}
	}()

	// First boot: kick an incremental scan shortly after start.
	go func() {
		time.Sleep(2 * time.Second)
		if !sc.Running() {
			sc.Start()
		}
	}()

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("mediatrans listening on %s", listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down…")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
