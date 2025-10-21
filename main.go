package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"

	"fortio.org/cli"
	"fortio.org/log"
	"fortio.org/terminal/ansipixels"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/table"
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

func PeerString(peer tsnet.Peer, peerData tsnet.PeerData) string {
	return fmt.Sprintf("%s%s%s (%s%s%s %s%d%s) %s%s%s",
		tcolor.BrightCyan.Foreground(), peer.Name, tcolor.Reset,
		tcolor.BrightGreen.Foreground(), peer.IP, tcolor.Reset,
		tcolor.Blue.Foreground(), peerData.Port, tcolor.Reset,
		tcolor.BrightYellow.Foreground(), peerData.HumanHash, tcolor.Reset)
}

var alignment = []table.Alignment{
	table.Right,  // Id
	table.Center, // Name
	table.Left,   // Ip
	table.Right,  // Port
	table.Right,  // Human Hash
}

func PeerLine(idx int, peer tsnet.Peer, peerData tsnet.PeerData) []string {
	return []string{
		strconv.Itoa(idx),
		tcolor.BrightCyan.Foreground() + peer.Name + tcolor.Reset,
		tcolor.BrightGreen.Foreground() + peer.IP + tcolor.Reset,
		fmt.Sprintf("%s%d%s", tcolor.Blue.Foreground(), peerData.Port, tcolor.Reset),
		tcolor.BrightYellow.Foreground() + peerData.HumanHash + tcolor.Reset,
	}
}

func OurLine(srv *tsnet.Server, ourIP, ourPort, humanID string) []string {
	return []string{
		"🏠",
		tcolor.Cyan.Foreground() + srv.Name + tcolor.Reset,
		tcolor.Green.Foreground() + ourIP + tcolor.Reset,
		tcolor.Blue.Foreground() + ourPort + tcolor.Reset,
		tcolor.Yellow.Foreground() + humanID + tcolor.Reset,
	}
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
	var version atomic.Uint64
	cfg := tsnet.Config{
		Name:   *fName,
		Port:   *fPort,
		Mcast:  *fMcast,
		Target: *fTarget,
		OnChange: func(v uint64) {
			version.Store(v)
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
	prev := ^uint64(0)
	ourAddress := srv.OurAddress()
	ourIP := ourAddress.IP.String()
	ourPort := strconv.Itoa(ourAddress.Port)
	ourLine := OurLine(srv, ourIP, ourPort, id.HumanID())
	ap.OnResize = func() error {
		prev = ^uint64(0) // force repaint
		return nil
	}
	err = ap.FPSTicks(func() bool {
		// Only refresh if we had (log) output or something changed, so cursor blinks (!).
		logHadOutput := ap.FlushLogger()
		if srv.Stopped() {
			return false
		}
		curVersion := version.Load()
		// log.Debugf("Have %d peers (prev %d), logHadOutput=%v", numPeers, prev, logHadOutput)
		if logHadOutput || curVersion != prev {
			if !logHadOutput {
				ap.StartSyncMode()
			}
			prev = curVersion
			lines := make([][]string, 0, srv.Peers.Len()+2) // +2 lines; note len may actually change but it's ok.
			lines = append(lines, ourLine, []string{
				tcolor.DarkGray.Foreground() + "Id" + tcolor.Reset,
				"🔗 " + tcolor.DarkGray.Foreground() + "Name" + tcolor.Reset,
				tcolor.DarkGray.Foreground() + "Ip" + tcolor.Reset,
				tcolor.DarkGray.Foreground() + "Port" + tcolor.Reset,
				tcolor.DarkGray.Foreground() + "Hash" + tcolor.Reset,
			})
			idx := 1
			for peer, peerData := range srv.Peers.AllSorted(tsnet.PeerSort) {
				lines = append(lines, PeerLine(idx, peer, peerData))
				idx++
			}
			table.WriteTable(ap, 0, alignment, 1, lines, table.BorderOuterColumns)
			ap.RestoreCursorPos()
			ap.EndSyncMode()
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
