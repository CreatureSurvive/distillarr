// mediatrans server: media analysis, transcoding & re-encoding.
package main

import (
	"context"
	"embed"
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
	"mediatrans/internal/jellyfin"
	"mediatrans/internal/jobs"
	"mediatrans/internal/preview"
	"mediatrans/internal/replace"
	"mediatrans/internal/scan"
	"mediatrans/internal/store"
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

	// Preview notify is bound to the hub once the API server exists.
	var hubNotify func(string, any)
	pv := preview.NewManager(prevRoot, eng.AcquireSem,
		func(event string, data any) {
			if hubNotify != nil {
				hubNotify(event, data)
			}
		})
	srv := api.NewServer(st, cfg, sc, eng, pv, webFS())
	hubNotify = srv.Hub().Broadcast

	// Jellyfin post-replace hook: refresh the item and restore its
	// DateCreated from the original file's birth time (Linux can't
	// rewrite crtime, so this keeps Jellyfin's "date added" intact).
	eng.OnReplaced = func(path string, oldStat *replace.SrcStat) {
		c := cfg.Get()
		if c.JellyfinURL == "" || c.JellyfinAPIKey == "" {
			return
		}
		row, err := st.JellyfinByPath(path)
		if err != nil || row == nil {
			return
		}
		cl := jellyfin.New(c.JellyfinURL, c.JellyfinAPIKey)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := cl.Refresh(ctx, row.ItemID); err != nil {
			log.Printf("jellyfin refresh %s: %v", path, err)
		}
		if oldStat != nil && oldStat.BtimeSec > 0 {
			bt := time.Unix(oldStat.BtimeSec, oldStat.BtimeNsec)
			if err := cl.PatchDateCreated(ctx, row.ItemID, bt); err != nil {
				log.Printf("jellyfin DateCreated patch %s: %v", path, err)
			}
		}
	}

	eng.Start()
	defer eng.Stop()

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
