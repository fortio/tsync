package main

import (
	"context"
	"flag"
	"os"

	"fortio.org/cli"
	"fortio.org/log"
	"fortio.org/terminal"
	"fortio.org/terminal/ansipixels"
	"fortio.org/tsync/tsnet"
)

func main() {
	os.Exit(Main())
}

func Main() int {
	fName := flag.String("name", "", "Name to use for this machine instead of the hostname")
	// echo -n "ts" | od -d -> 29556
	fPort := flag.Int("port", 29556, "Port to use")
	// 239.255."t"."s"
	fMcast := flag.String("mcast", "239.255.116.115", "Multicast address to use for server discovery")
	fTarget := flag.String("target", tsnet.DefaultTarget, "Test target udp ip:port to use to find the right interface and local ip")
	cli.Main()
	ap := ansipixels.NewAnsiPixels(20)
	if err := ap.Open(); err != nil {
		return 1 // error already logged
	}
	defer ap.Restore()
	crlfWriter := &terminal.CRLFWriter{Out: os.Stdout}
	terminal.LoggerSetup(crlfWriter)
	cfg := tsnet.Config{
		Name:   *fName,
		Port:   *fPort,
		Mcast:  *fMcast,
		Target: *fTarget,
	}
	srv := cfg.NewServer()
	if err := srv.Start(context.Background()); err != nil {
		return log.FErrf("Failed to start tsync server: %v", err)
	}
	defer srv.Stop()
	log.Infof("Started tsync with name %q", srv.Name)
	log.Infof("Press Q, q or Ctrl-C to stop")
	for {
		if err := ap.ReadOrResizeOrSignal(); err != nil {
			log.Infof("Exiting on %v", err)
			return 1
		}
		c := ap.Data[0]
		switch c {
		case 'q', 'Q', 3: // Ctrl-C
			log.Infof("Exiting on %q", c)
			return 0
		default:
			log.Infof("Got %q", c)
		}
	}
}
