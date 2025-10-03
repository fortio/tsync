package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"fortio.org/cli"
	"fortio.org/log"
	"fortio.org/sets"
	"fortio.org/terminal/ansipixels"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

func main() {
	os.Exit(Main())
}

func LoadIdentity() (*tcrypto.Identity, error) {
	storage, err := tcrypto.InitStorage()
	if err != nil {
		return nil, err
	}
	// Try to load existing identity
	op := "Loaded"
	level := log.Info
	id, err := storage.LoadIdentity()
	if err != nil {
		log.Infof("No existing identity found, creating new one: %v", err)
		id, err = tcrypto.NewIdentity()
		if err != nil {
			return nil, err
		}
		err = storage.SaveIdentity(id)
		if err != nil {
			return nil, err
		}
		op = "Created"
		level = log.Warning
	}
	log.Logf(level, "%s identity with public key: %s", op, id.PublicKeyToString())
	return id, nil
}

func Main() int {
	fName := flag.String("name", "", "Name to use for this machine instead of the hostname")
	// echo -n "ts" | od -d -> 29556
	fPort := flag.Int("port", 29556, "Port to use")
	// 239.255."t"."s"
	fMcast := flag.String("mcast", "239.255.116.115", "Multicast address to use for server discovery")
	fTarget := flag.String("target", tsnet.DefaultTarget, "Test target udp ip:port to use to find the right interface and local ip")
	fInterval := flag.Duration("interval", tsnet.DefaultBroadcastInterval,
		"Base interval in milliseconds between broadcasts (before [0-1]s jitter)")
	cli.Main()
	ap := ansipixels.NewAnsiPixels(60)
	if err := ap.Open(); err != nil {
		return 1 // error already logged
	}
	defer ap.Restore()
	id, err := LoadIdentity()
	if err != nil {
		return log.FErrf("Failed to load or create identity: %v", err)
	}
	peers := sets.New[string]()
	var mutex sync.Mutex
	cfg := tsnet.Config{
		Name:   *fName,
		Port:   *fPort,
		Mcast:  *fMcast,
		Target: *fTarget,
		OnNewPeer: func(peer tsnet.Peer) {
			pub, err1 := tcrypto.IdentityPublicKeyString(peer.PublicKey)
			if err1 != nil {
				log.Errf("Failed to decode peer %q public key %q: %v", peer.Name, peer.PublicKey, err1)
				return
			}
			id := tcrypto.HumanHash(pub)
			mutex.Lock()
			peers.Add(fmt.Sprintf("%s%s%s (%s%s%s %s%d%s) %s%s%s",
				tcolor.BrightCyan.Foreground(), peer.Name, tcolor.Reset,
				tcolor.BrightGreen.Foreground(), peer.IP, tcolor.Reset,
				tcolor.Blue.Foreground(), peer.Port, tcolor.Reset,
				tcolor.BrightYellow.Foreground(), id, tcolor.Reset))
			mutex.Unlock()
		},
		Identity:              id,
		BaseBroadcastInterval: *fInterval,
	}
	srv := cfg.NewServer()
	if err = srv.Start(context.Background()); err != nil {
		return log.FErrf("Failed to start tsync server: %v", err)
	}
	defer srv.Stop()
	log.Infof("Started tsync with name %q", srv.Name)
	log.Infof("Press Q, q or Ctrl-C to stop")
	ap.AutoSync = false
	prev := 0
	var buf strings.Builder
	ourAddress := srv.OurAddress()
	ourIP := ourAddress.IP.String()
	ourPort := ourAddress.Port
	ourLine := fmt.Sprintf("🏠\n%s%s%s (%s%s%s %s%d%s) %s%s%s",
		tcolor.Cyan.Foreground(), srv.Name, tcolor.Reset,
		tcolor.Green.Foreground(), ourIP, tcolor.Reset,
		tcolor.Blue.Foreground(), ourPort, tcolor.Reset,
		tcolor.Yellow.Foreground(), id.HumanID(), tcolor.Reset,
	)
	err = ap.FPSTicks(context.Background(), func(_ context.Context) bool {
		// Only refresh if we had (log) output or something changed, so cursor blinks (!).
		logHadOutput := ap.FlushLogger()
		mutex.Lock()
		numPeers := peers.Len()
		if logHadOutput || numPeers != prev {
			if !logHadOutput {
				ap.StartSyncMode()
			}
			prev = numPeers
			newPeers := peers.Clone()
			mutex.Unlock()
			for _, p := range sets.Sort(newPeers) {
				fmt.Fprintf(&buf, "\n%s", p)
			}
			ap.WriteBoxed(1, "%s\n🔗%s", ourLine, buf.String())
			buf.Reset()
			ap.RestoreCursorPos()
			ap.EndSyncMode()
		} else {
			mutex.Unlock()
		}
		if len(ap.Data) == 0 {
			return true
		}
		c := ap.Data[0]
		switch c {
		case 'q', 'Q', 3: // Ctrl-C
			log.Infof("Exiting on %q", c)
			return false
		default:
			log.Infof("Got %q", c)
		}
		return true
	})
	if err != nil {
		log.Infof("Exiting on %v", err)
		return 1
	}
	return 0
}
