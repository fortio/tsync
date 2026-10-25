package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"fortio.org/cli"
	"fortio.org/log"
	"fortio.org/progressbar"
	"fortio.org/smap"
	"fortio.org/terminal/ansipixels"
	"fortio.org/terminal/ansipixels/tcolor"
	"fortio.org/tsync/syncdir"
	"fortio.org/tsync/tcrypto"
	"fortio.org/tsync/tsnet"
)

func main() {
	os.Exit(Main())
}

func LoadIdentity() (*tcrypto.Identity, *tcrypto.Storage, error) {
	storage, err := tcrypto.InitStorage()
	if err != nil {
		return nil, nil, err
	}
	// Try to load existing identity
	op := "Loaded"
	level := log.Info
	id, err := storage.LoadIdentity()
	if err != nil {
		log.Infof("No existing identity found, creating new one: %v", err)
		id, err = tcrypto.NewIdentity()
		if err != nil {
			return nil, nil, err
		}
		err = storage.SaveIdentity(id)
		if err != nil {
			return nil, nil, err
		}
		op = "Created"
		level = log.Warning
	}
	log.Logf(level, "%s identity with public key: %s", op, id.PublicKeyToString())
	return id, storage, nil
}

var alignment = []ansipixels.Alignment{
	ansipixels.Right,  // Id
	ansipixels.Center, // Name
	ansipixels.Left,   // Ip
	ansipixels.Right,  // Port
	ansipixels.Right,  // Human Hash
}

func PeerLine(idx int, peer tsnet.Peer, peerData tsnet.PeerData, linked bool) []string {
	idxStr := strconv.Itoa(idx)
	switch peerData.Status {
	case tsnet.NotLinked:
		// leave uncolored
	case tsnet.SentConn:
		idxStr = tcolor.Inverse + Color16(tcolor.BrightYellow, idxStr)
	case tsnet.ReceivedConn:
		idxStr = tcolor.Blink + tcolor.Inverse + Color16(tcolor.BrightBlue, idxStr)
	case tsnet.Failed:
		idxStr = tcolor.Inverse + Color16(tcolor.BrightRed, idxStr)
	case tsnet.Connected:
		idxStr = tcolor.Inverse + Color16(tcolor.BrightGreen, idxStr)
	}
	name := SanitizeName(peer.Name)
	if linked {
		name = "🔗 " + name
	}
	if !peerData.Compatible() {
		name += " (old version)"
	}
	return []string{
		idxStr,
		Color16(tcolor.BrightCyan, name),
		Color16(tcolor.BrightGreen, peer.IP),
		Color16f(tcolor.Blue, "%d", peerData.Port),
		Color16(tcolor.BrightYellow, peerData.HumanHash),
	}
}

// HeaderLine is the peers table header.
func HeaderLine() []string {
	return []string{DarkGray("Id"), "🔗 " + DarkGray("Name"), DarkGray("Ip"), DarkGray("Port"), DarkGray("Hash")}
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

// CheckFiles checks that all the files can be offered.
func CheckFiles(files []string) error {
	for _, f := range files {
		fi, err := tsnet.CheckFile(f)
		if err != nil {
			return fmt.Errorf("%q: %w", f, err)
		}
		if fi.IsDir() {
			log.LogVf("Directory %q ok to offer", f)
			continue
		}
		size := progressbar.HumanBytes(fi.Size())
		if fi.Size() >= 1024 {
			size += fmt.Sprintf(" (%d bytes)", fi.Size())
		}
		log.LogVf("File %q ok to offer: %s", f, size)
	}
	return nil
}

// SplitList splits a comma separated list, ignoring empty entries and spaces.
func SplitList(s string) []string {
	var res []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			res = append(res, p)
		}
	}
	return res
}

// ParseRate parses a rate in bytes per second like "10MB/s", "500k" or "1024" (case insensitive,
// K, M, G are powers of 1024 like elsewhere in tsync, the /s is optional). Empty is 0 (unlimited).
func ParseRate(s string) (int64, error) {
	v, err := ParseSize(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "/s"))
	if err != nil {
		return 0, fmt.Errorf("can't parse %q as a rate (eg 10MB/s)", s)
	}
	return v, nil
}

// ParseSize parses a size in bytes like "1GB", "500k" or "1024" (case insensitive, K, M, G are powers of
// 1024). Empty is 0.
func ParseSize(s string) (int64, error) {
	orig := s
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	s = strings.TrimSuffix(s, "b")
	mult := int64(1)
	if n := len(s); n > 0 {
		if i := strings.IndexByte("kmg", s[n-1]); i >= 0 {
			mult = int64(1) << (10 * (i + 1))
			s = s[:n-1]
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("can't parse %q as a size (eg 1GB)", orig)
	}
	return max(1, int64(v*float64(mult))), nil
}

func logStartup(srv *tsnet.Server, cfg *tsnet.Config) {
	log.Infof("Started tsync %s with name %q, received files are saved in %s, max send rate: %s",
		cli.LongVersion, srv.Name, cfg.FileDir, RateString(cfg.MaxRate))
	for _, f := range cfg.Sync {
		log.Infof("Sync folder %q: %s (keeping %d replaced versions per file, trash pruned to %s)",
			f.Name, f.Path, cfg.SyncKeep, progressbar.HumanBytes(cfg.SyncTrashMax))
	}
}

// RateString is the rate in human form (eg 50 Kb/s) or "unlimited" if there is no limit.
func RateString(rate int64) string {
	if rate <= 0 {
		return "unlimited"
	}
	return progressbar.HumanBytes(rate) + "/s"
}

// SizeFlag defines a size flag (see ParseSize) and returns a pointer to its value in bytes.
func SizeFlag(name string, value int64, usage string) *int64 {
	flag.Func(name, usage, func(s string) (err error) {
		value, err = ParseSize(s)
		return err
	})
	return &value
}

// SyncFlags defines the sync flags: folders, replaced versions to keep and trash size.
func SyncFlags() (*[]tsnet.SyncFolder, *int, *int64) {
	folders := SyncFlag("sync")
	keep := flag.Int("sync-keep", 3, "How many replaced versions of each file sync keeps in the folder's "+
		syncdir.TrashDir+" (0 for none)")
	trashMax := SizeFlag("sync-trash-max", 1<<30, "Size the trash of a sync folder is pruned to, the latest "+
		"version of each file is always kept (0 for no limit)")
	return folders, keep, trashMax
}

// SyncFlag defines the (repeatable) sync folder flag, see ParseSyncFolder.
func SyncFlag(name string) *[]tsnet.SyncFolder {
	var folders []tsnet.SyncFolder
	flag.Func(name, "Sync the content of the directory `label=path` with the linked peers that use the same label"+
		" (the directory can be anywhere and named anything on each machine). Repeatable. Files changed on both"+
		" sides are left alone (conflicts), deletions aren't propagated",
		func(s string) error {
			f, err := ParseSyncFolder(s)
			if err != nil {
				return err
			}
			for _, o := range folders {
				if o.Name == f.Name {
					return fmt.Errorf("duplicate sync folder name %q", f.Name)
				}
			}
			folders = append(folders, f)
			return nil
		})
	return &folders
}

// RateFlag defines a rate flag (see ParseRate) and returns a pointer to its value in bytes per second.
func RateFlag(name string) *int64 {
	var rate int64
	flag.Func(name, "Max file sending `rate` (eg 10MB/s, 1k), K, M, G are powers of 1024 bytes. Default unlimited",
		func(s string) (err error) {
			rate, err = ParseRate(s)
			return err
		})
	return &rate
}

// SendPastes sends the completed pastes to the linked peers (dropped files are offered),
// returns true if anything changed.
func SendPastes(srv *tsnet.Server, clips *ClipPanel, files *FilePanel, pastes []Paste) bool {
	sent := false
	for _, p := range pastes {
		if p.Truncated {
			Notices.Errf("Paste too large (over %s), not sent", sizeStr(tsnet.MaxClipSize))
			continue
		}
		if paths, ok := DroppedFiles(p.Text); ok {
			files.Offer(paths)
			sent = true
			continue
		}
		text := SanitizeClip(p.Text)
		n, err := srv.SendClip(text)
		if err != nil {
			Notices.Errf("Error sending clipboard: %v", err)
		}
		log.Infof("Sent %s to %d peers", sizeStr(len(text)), n)
		clips.SetOurs(text, n)
		sent = true
	}
	return sent
}

// TopArea is the fixed (non log) top part of the screen: peers table, clip boxes and help/separator line.
type TopArea struct {
	ap         *ansipixels.AnsiPixels
	srv        *tsnet.Server
	logArea    *LogArea
	clips      *ClipPanel
	files      *FilePanel
	fixedLines [][]string // our line and header.
	syncs      *SyncPanel
	help       string
	pending    map[tsnet.Peer]bool // peers with a pending connection request (for the bell).
}

// PendingBanner returns the message to show when there are pending connection requests.
func PendingBanner(peers []smap.KV[tsnet.Peer, tsnet.PeerData]) (string, bool) {
	first := -1
	count := 0
	for i, kv := range peers {
		if kv.Value.Status != tsnet.ReceivedConn {
			continue
		}
		count++
		if first < 0 {
			first = i
		}
	}
	if count == 0 {
		return "", false
	}
	kv := peers[first]
	how := "click"
	if first < 9 {
		how += " or press " + strconv.Itoa(first+1)
	}
	msg := fmt.Sprintf(" ⚠ %s (%s) wants to connect: check the hash, then %s to accept ",
		SanitizeName(kv.Key.Name), kv.Value.HumanHash, how)
	if count > 1 {
		msg += fmt.Sprintf("(+%d more) ", count-1)
	}
	return msg, true
}

// bellOnNewPending rings the terminal bell (once) if there is a new pending connection request.
func (t *TopArea) bellOnNewPending(peers []smap.KV[tsnet.Peer, tsnet.PeerData]) {
	current := make(map[tsnet.Peer]bool)
	ring := false
	for _, kv := range peers {
		if kv.Value.Status == tsnet.ReceivedConn {
			current[kv.Key] = true
			ring = ring || !t.pending[kv.Key]
		}
	}
	t.pending = current
	if ring {
		t.ap.WriteString("\a")
	}
}

// Draw draws the top area for the given peers and returns the table width.
func (t *TopArea) Draw(peers []smap.KV[tsnet.Peer, tsnet.PeerData]) int {
	ap := t.ap
	lines := make([][]string, 0, len(peers)+len(t.fixedLines))
	lines = append(lines, t.fixedLines...)
	for i, kv := range peers {
		lines = append(lines, PeerLine(i+1, kv.Key, kv.Value, t.srv.Linked(kv.Key)))
	}
	tableHeight := len(lines) + 2 // + borders.
	avail := ap.H - MinLogLines - tableHeight - 1
	// Files first (pending offers matter most), clips get the rest.
	fileLines := t.files.Lines()
	if keep := max(0, avail-2); len(fileLines) > keep {
		fileLines = fileLines[len(fileLines)-keep:]
	}
	filesHeight := 0
	if len(fileLines) > 0 {
		filesHeight = len(fileLines) + 2
	}
	syncLines := t.syncs.Lines()
	if keep := max(0, avail-filesHeight-2); len(syncLines) > keep {
		syncLines = syncLines[:keep]
	}
	syncHeight := 0
	if len(syncLines) > 0 {
		syncHeight = len(syncLines) + 2
	}
	boxes := t.clips.Layout(peers, avail-filesHeight-syncHeight)
	boxesHeight := 0
	for _, b := range boxes {
		boxesHeight += b.height()
	}
	// table + clip boxes + files box + sync box + separator line.
	top := t.logArea.SetTop(tableHeight + boxesHeight + filesHeight + syncHeight + 1)
	for y := range top {
		ap.MoveCursor(0, y)
		ap.ClearEndOfLine()
	}
	tableWidth := ap.WriteTable(0, alignment, 1, lines, ansipixels.BorderOuterColumns)
	t.clips.Draw(tableHeight, boxes)
	t.files.Draw(tableHeight+boxesHeight, fileLines)
	t.syncs.Draw(tableHeight+boxesHeight+filesHeight, syncLines)
	if top > 0 {
		ap.MoveCursor(0, top-1)
		t.drawSeparator(peers)
	}
	t.bellOnNewPending(peers)
	return tableWidth
}

// drawSeparator draws the bottom line of the top area: pending banner, else notice, else help.
func (t *TopArea) drawSeparator(peers []smap.KV[tsnet.Peer, tsnet.PeerData]) {
	ap := t.ap
	msg := t.help
	style := ""
	if nmsg, ncolor, ok := Notices.Current(); ok {
		msg, style = nmsg, tcolor.Inverse+ncolor.Foreground()
	}
	if banner, pending := PendingBanner(peers); pending {
		msg, style = banner, tcolor.Inverse+tcolor.BrightYellow.Foreground()
	}
	h, hw := TruncateRightToFit(ap, msg, ap.W-4)
	left := (ap.W - hw) / 2
	if style != "" {
		h = style + h + tcolor.Reset
	}
	ap.WriteString(DarkGray(strings.Repeat("─", left)) + h + DarkGray(strings.Repeat("─", ap.W-left-hw)))
}

// HandleKey handles a key press, returns false to exit.
func HandleKey(c byte, srv *tsnet.Server, clips *ClipPanel, files *FilePanel,
	peers []smap.KV[tsnet.Peer, tsnet.PeerData],
) bool {
	switch c {
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		connectToPeerIdx := int(c - '0')
		maxPeerIdx := len(peers)
		if connectToPeerIdx <= maxPeerIdx {
			peer := peers[connectToPeerIdx-1]
			InitiatePeerConnection(srv, peer.Key, peer.Value)
		} else {
			Notices.Warnf("No peer with index %d to connect to (max %d).", connectToPeerIdx, maxPeerIdx)
		}
	case 'd':
		files.DownloadLatest()
	case 'c':
		if box, ok := clips.Latest(); ok {
			clips.Copy(box)
		} else {
			Notices.Warnf("No received clipboard to copy yet")
		}
	case 'q', 'Q', 3: // Ctrl-C
		log.Infof("Exiting on %q", c)
		return false
	default:
		log.Infof("Input %q", c)
	}
	return true
}

func InitiatePeerConnection(srv *tsnet.Server, peer tsnet.Peer, peerData tsnet.PeerData) {
	if peerData.Status == tsnet.ReceivedConn {
		log.Infof("Accepting connection from peer %q at %s:%d (%s)", peer.Name, peer.IP, peerData.Port, peerData.HumanHash)
	} else {
		log.Infof("Initiating connection to peer %q at %s:%d", peer.Name, peer.IP, peerData.Port)
	}
	if connErr := srv.ConnectToPeer(peer); connErr != nil {
		Notices.Errf("Failed to connect to peer %s: %v", peer.Name, connErr)
	}
}

// MouseInsideBox returns whether the mouse is inside the box and the index of the line inside the box.
func MouseInsideBox(ap *ansipixels.AnsiPixels, tableWidth, numPeers int) (int, bool) {
	tableWidth -= 2                       // remove the borders
	startTable := (ap.W-tableWidth)/2 + 1 // mouse coordinates start at 1
	endTable := startTable + tableWidth
	log.LogVf("MouseInsideBox: ap.Mx=%d, ap.My=%d, tableWidth=%d, startTable=%d, endTable=%d, numPeers=%d",
		ap.Mx, ap.My, tableWidth, startTable, endTable, numPeers)
	if ap.Mx < startTable || ap.Mx >= endTable {
		return -1, false
	}
	line := ap.My - 4 // accounts for border, our line and header and mouse coordinates starting at 1
	if line >= 0 && line < numPeers {
		return line, true
	}
	return -1, false
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
	fClipLines := flag.Int("clip-lines", 3, "Max number of lines shown for each clipboard box")
	fAutoConnect := flag.Bool("auto-connect", true, "Automatically connect to trusted (previously accepted) peers")
	fDir := flag.String("dir", "",
		"Directory where received files are saved (default ~/Downloads if it exists, else current directory)")
	fPeers := flag.String("peers", "",
		"Comma separated `host[:port]` list to also send discovery to directly, when multicast doesn't work (eg VMs, VPNs)")
	fMaxRate := RateFlag("max-rate")
	fSync, fSyncKeep, fSyncTrashMax := SyncFlags()
	cli.ArgsHelp = "[file...]"
	cli.MaxArgs = -1
	cli.Main()
	// Files to offer (eg from a ssh session where drag and drop isn't possible): check them before starting.
	if err := CheckFiles(flag.Args()); err != nil {
		return log.FErrf("Can't offer %v", err)
	}
	ap := ansipixels.NewAnsiPixels(60)
	if err := ap.Open(); err != nil {
		return 1 // error already logged
	}
	ap.MouseClickOn()
	ap.SetBracketedPasteMode(true)
	logArea := NewLogArea(ap)
	defer func() {
		logArea.Reset()
		ap.SetBracketedPasteMode(false)
		ap.MouseClickOff()
		ap.Restore()
	}()
	id, storage, err := LoadIdentity()
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
		Storage:               storage,
		NoAutoConnect:         !*fAutoConnect,
		Peers:                 SplitList(*fPeers),
		FileDir:               DownloadDir(*fDir),
		MaxRate:               *fMaxRate,
		Sync:                  *fSync,
		SyncKeep:              *fSyncKeep,
		SyncTrashMax:          *fSyncTrashMax,
	}
	srv := cfg.NewServer()
	if err = srv.Start(context.Background()); err != nil {
		return log.FErrf("Failed to start tsync server: %v", err)
	}
	defer srv.Stop()
	logStartup(srv, &cfg)
	log.Infof("Paste to send to connected peers, Q, q or Ctrl-C to stop")
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
	var peersSnapshot []smap.KV[tsnet.Peer, tsnet.PeerData]
	tableWidth := 0
	clips := NewClipPanel(ap, *fClipLines)
	files := NewFilePanel(ap, srv, cfg.FileDir, 5)
	files.Offer(flag.Args())
	ap.OnMouse = func() {
		if !ap.LeftClick() || !ap.MouseRelease() {
			return
		}
		if peerLine, ok := MouseInsideBox(ap, tableWidth, len(peersSnapshot)); ok {
			peer := peersSnapshot[peerLine]
			log.Infof("Left click (release) at %d,%d -> line %d - %q", ap.Mx, ap.My, peerLine+1, peer.Key.Name)
			InitiatePeerConnection(srv, peer.Key, peer.Value)
		} else if box, ok := clips.Clicked(ap.My); ok {
			clips.Copy(box)
		} else if files.Clicked(ap.My) {
			return
		} else {
			log.Infof("Left click (release) at %d,%d -> outside peer list", ap.Mx, ap.My)
		}
	}
	// We decode mouse events ourselves, outside of pastes.
	ap.NoDecode = true
	paste := &PasteCollector{MaxSize: tsnet.MaxClipSize + 1}
	top := &TopArea{
		ap: ap, srv: srv, logArea: logArea, clips: clips, files: files,
		fixedLines: [][]string{ourLine, HeaderLine()},
		syncs:      NewSyncPanel(ap, srv),
		help:       " paste/drop file: send · click box or c: copy · d: download · click or 1-9: connect/accept · q: quit ",
	}
	err = ap.FPSTicks(func() bool {
		// Log output scrolls in its own area (see LogArea) so we only need to refresh
		// the top area when something changed, so cursor blinks (!).
		logHadOutput := ap.FlushLogger()
		if srv.Stopped() {
			if logHadOutput {
				ap.EndSyncMode()
			}
			return false
		}
		other, pastes := paste.Feed(ap.Data)
		ap.Data = other
		ap.MouseDecodeAll()
		if SendPastes(srv, clips, files, pastes) || Notices.NeedsRepaint() {
			prev = ^uint64(0) // force repaint
		}
		curVersion := version.Load()
		if curVersion != prev {
			if !logHadOutput {
				ap.StartSyncMode()
			}
			prev = curVersion
			peersSnapshot = srv.Peers.KeysValuesSnapshot()
			slices.SortFunc(peersSnapshot, tsnet.PeerKVSort)
			tableWidth = top.Draw(peersSnapshot)
			ap.RestoreCursorPos()
			ap.EndSyncMode()
		} else if logHadOutput {
			ap.EndSyncMode()
		}
		if len(ap.Data) == 0 {
			return true
		}
		return HandleKey(ap.Data[0], srv, clips, files, peersSnapshot)
	})
	if err != nil {
		log.Infof("Exiting on %v", err)
		return 1
	}
	return 0
}
