package main

import (
	"context"
	"errors"
	"time"

	"fortio.org/cli"
	"fortio.org/log"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/tsnet"
)

// HeadlessCheck returns the error if the flags and arguments can't be used with -headless.
func HeadlessCheck(files []string, syncFolders int) error {
	if len(files) > 0 {
		return errors.New("-headless can't offer files, only sync")
	}
	if syncFolders == 0 {
		return errors.New("-headless is for directory sync, needs at least one -sync label=path")
	}
	return nil
}

// RunHeadless runs the sync (only) without a UI, until interrupted. Only already trusted peers can link
// (pair once with the UI first). cfg has the flags values, other than the identity and sync folders (fSync).
func RunHeadless(files []string, cfg tsnet.Config, fSync syncFlags, tsyncDir string) int {
	if err := HeadlessCheck(files, len(*fSync.folders)); err != nil {
		return log.FErrf("%v", err)
	}
	id, storage, err := LoadIdentity(ExpandHome(tsyncDir))
	if err != nil {
		return log.FErrf("Failed to load or create identity: %v", err)
	}
	cfg.Identity, cfg.Storage = id, storage
	cfg.Headless, cfg.NoAutoConnect = true, false
	fSync.Apply(&cfg)
	// Goroutines are started: put back their ids in the logs (see cli.UntilInterrupted).
	log.Config.GoroutineID = true
	srv := cfg.NewServer()
	if err = srv.Start(context.Background()); err != nil {
		return log.FErrf("Failed to start tsync server: %v", err)
	}
	defer srv.Stop()
	logStartup(srv, &cfg)
	log.Infof("Headless: syncing with trusted peers only, Ctrl-C or SIGTERM to stop")
	done := make(chan struct{})
	go func() {
		cli.UntilInterrupted()
		close(done)
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	logged := make(map[string]string)
	for {
		select {
		case <-done:
			return 0
		case <-ticker.C:
			if srv.Stopped() {
				return 0 // eg replaced by a newer instance, already logged.
			}
			LogSyncStatus(srv, logged)
		}
	}
}

// LogSyncStatus logs the sync folders states that changed since last time (logged has what was logged for each).
func LogSyncStatus(srv *tsnet.Server, logged map[string]string) {
	for _, st := range srv.SyncStatus() {
		key := st.Folder + "\x00" + st.Peer.PublicKey
		line := SyncLine(st)
		text := line.text
		if st.Getting > 0 { // not each progress step.
			text = "📁 " + st.Folder + ": downloading from " + SanitizeName(st.Peer.Name)
		}
		if logged[key] == text {
			continue
		}
		logged[key] = text
		switch line.color {
		case tcolor.BrightRed:
			log.Errf("%s", text)
		case tcolor.BrightYellow:
			log.Warnf("%s", text)
		default:
			log.Infof("%s", text)
		}
	}
}
