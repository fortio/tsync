package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync/atomic"

	"fortio.org/cli"
	"fortio.org/log"
	"fortio.org/smap"
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

var alignment = []table.Alignment{
	table.Right,  // Id
	table.Center, // Name
	table.Left,   // Ip
	table.Right,  // Port
	table.Right,  // Human Hash
}

func PeerLine(idx int, peer tsnet.Peer, peerData tsnet.PeerData) []string {
	idxStr := strconv.Itoa(idx)
	switch peerData.Status {
	case tsnet.NotLinked:
		// leave uncolored
	case tsnet.Connecting:
		idxStr = tcolor.Inverse + Color16(tcolor.BrightYellow, idxStr)
	case tsnet.Failed:
		idxStr = tcolor.Inverse + Color16(tcolor.BrightRed, idxStr)
	case tsnet.Connected:
		idxStr = tcolor.Inverse + Color16(tcolor.BrightGreen, idxStr)
	}
	return []string{
		idxStr,
		Color16(tcolor.BrightCyan, peer.Name),
		Color16(tcolor.BrightGreen, peer.IP),
		Color16f(tcolor.Blue, "%d", peerData.Port),
		Color16(tcolor.BrightYellow, peerData.HumanHash),
	}
}

func OurLine(srv *tsnet.Server, ourIP, ourPort, humanID string) []string {
	return []string{
		"🏠",
		Color16(tcolor.Cyan, srv.Name),
		Color16(tcolor.Green, ourIP),
		Color16(tcolor.Blue, ourPort),
		Color16(tcolor.Yellow, humanID),
	}
}

// Color16 returns a colored string.
func Color16(color tcolor.BasicColor, s string) string {
	return color.Foreground() + s + tcolor.Reset
}

// Color16f returns a colored string with printf-style formatting.
func Color16f(color tcolor.BasicColor, format string, args ...any) string {
	return Color16(color, fmt.Sprintf(format, args...))
}

func DarkGray(s string) string {
	return Color16(tcolor.DarkGray, s)
}

func InitiatePeerConnection(srv *tsnet.Server, peer tsnet.Peer, peerData tsnet.PeerData) {
	log.Infof("Initiating connection to peer %q at %s:%d", peer.Name, peer.IP, peerData.Port)
	if connErr := srv.ConnectToPeer(peer); connErr != nil {
		log.Errf("Failed to connect to peer %s: %v", peer.Name, connErr)
	}
}

func Main() int {
	fName := flag.String("name", "", "Name to use for this machine instead of the hostname")
	// echo -n "ts" | od -d -> 29556
	fPort := flag.Int("port", tsnet.DefaultDiscoveryPort, "Discovery port to use")
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
	ap.MouseClickOn()
	defer func() {
		ap.MouseClickOff()
		ap.Restore()
	}()
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
	headerLine := []string{
		DarkGray("Id"),
		"🔗 " + DarkGray("Name"),
		DarkGray("Ip"),
		DarkGray("Port"),
		DarkGray("Hash"),
	}
	ap.OnResize = func() error {
		prev = ^uint64(0) // force repaint
		return nil
	}
	var peersSnapshot []smap.KV[tsnet.Peer, tsnet.PeerData]
	ap.OnMouse = func() {
		if !ap.LeftClick() || !ap.MouseRelease() {
			return
		}
		line := ap.My - 4 // account for our line and header
		if line >= 0 && line < len(peersSnapshot) {
			peer := peersSnapshot[line]
			log.Infof("Left click (release) at %d,%d -> line %d - connecting to %q", ap.Mx, ap.My, line+1, peer.Key.Name)
			InitiatePeerConnection(srv, peer.Key, peer.Value)
		} else {
			log.Infof("Left click (release) at %d,%d -> line %d - no peer", ap.Mx, ap.My, line+1)
		}
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
			peersSnapshot = srv.Peers.KeysValuesSnapshot()
			slices.SortFunc(peersSnapshot, tsnet.PeerKVSort)
			lines := make([][]string, 0, len(peersSnapshot)+2)
			lines = append(lines, ourLine, headerLine)
			idx := 1
			for _, kv := range peersSnapshot {
				lines = append(lines, PeerLine(idx, kv.Key, kv.Value))
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
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			connectToPeerIdx := int(c - '0')
			maxPeerIdx := len(peersSnapshot)
			if connectToPeerIdx <= maxPeerIdx {
				peer := peersSnapshot[connectToPeerIdx-1]
				InitiatePeerConnection(srv, peer.Key, peer.Value)
			} else {
				log.Warnf("No peer with index %d to connect to (max %d).", connectToPeerIdx, maxPeerIdx)
			}
		case 'q', 'Q', 3: // Ctrl-C
			log.Infof("Exiting on %q", c)
			return false
		default:
			log.Infof("Input %q", c)
		}
		return true
	})
	if err != nil {
		log.Infof("Exiting on %v", err)
		return 1
	}
	return 0
}
