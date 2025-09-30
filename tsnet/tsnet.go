// Network library support for tsync (discovery/registration and communication).
package tsnet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"

	"fortio.org/log"
)

const (
	// Max size of messages.
	BufSize = 576 - 60 - 8 // 576 byte IP packet - 60 byte IP header - 8 byte UDP header = 508 bytes
	// What udp address we try by default to find our interface and ip.
	DefaultTarget = "8.8.8.8:53"
)

type Config struct {
	// Name to use, if empty hostname will be used.
	Name  string
	Port  int
	Mcast string
	// Which ip:port we try to resolve to find our address and interface.
	Target string
}

type Server struct {
	// Our copy of the input config.
	Config
	// internal stuff
	addr            *net.UDPAddr
	broadcastListen *net.UDPConn
	broadcastSend   *net.UDPConn
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

func (c *Config) NewServer() *Server {
	return &Server{Config: *c}
}

func (s *Server) Start(ctx context.Context) error {
	var err error
	if s.Name == "" {
		s.Name, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	if s.Target == "" {
		s.Target = DefaultTarget
	}
	addr := fmt.Sprintf("%s:%d", s.Mcast, s.Port)
	s.addr, err = net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	log.Infof("Starting tsync server %q on %s -> %s", s.Name, addr, s.addr)
	// Try to get the right interface to listen on
	goodIf, localIP, err := GetInternetInterface(s.Target)
	if err != nil {
		log.Warnf("Could not get default route interface using %q as test destination, will listen on all: %v", s.Target, err)
	} else {
		log.Infof("Using interface %q for multicast (with local IP %v)", goodIf.Name, localIP)
	}
	s.broadcastListen, err = net.ListenMulticastUDP("udp4", goodIf, s.addr)
	if err != nil {
		return err
	}
	s.broadcastSend, err = net.DialUDP("udp4", localIP, s.addr)
	if err != nil {
		s.broadcastListen.Close()
		return err
	}
	// get a cancelable context
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(2)
	go s.runAdv(ctx)
	go s.runReceive(ctx)
	return nil
}

func (s *Server) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.cancel = nil
	s.broadcastListen.Close() // needed or write will block forever
	s.wg.Wait()
	s.broadcastSend.Close()
}

func (s *Server) runAdv(ctx context.Context) {
	defer s.wg.Done()
	// 1 sec tick + some random jitter
	jitter := rand.IntN(1024) //nolint:gosec // not cryptographic
	interval := time.Second + time.Duration(jitter)*time.Millisecond
	ticker := time.NewTicker(interval)
	log.Infof("Starting tsync broadcast sender %q (%v) with %v interval (jitter %d ms)",
		s.Name, s.broadcastSend.LocalAddr(), interval, jitter)
	defer ticker.Stop()
	epoch := 0
	for {
		select {
		case <-ctx.Done():
			log.Infof("Exiting tsync sender %q after %d ticks (%v)", s.Name, epoch, ctx.Err())
			return
		case <-ticker.C:
			epoch++
			log.Infof("Tick %d", epoch)
			_, err := fmt.Fprintf(s.broadcastSend, "tsync %s epoch %d", s.Name, epoch)
			if err != nil {
				log.Errf("Error sending UDP packet: %v", err)
			}
		}
	}
}

func (s *Server) runReceive(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, BufSize)
	log.Infof("Starting tsync broadcast receiver %q on %s with %d bytes buffer",
		s.Name, s.broadcastListen.LocalAddr(), BufSize)
	for {
		select {
		case <-ctx.Done():
			log.Infof("Exiting tsync receiver after %v", ctx.Err())
			return
		default:
			// we rely on Stop() closing the socket to unblock ReadFromUDP on exit.
			n, addr, err := s.broadcastListen.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					log.Infof("Normal read from closed error on exit: %v", err)
				} else {
					log.Errf("Error receiving UDP packet: %v", err)
				}
				continue
			}
			log.Infof("Received %d bytes from %v: %q", n, addr, buf[:n])
		}
	}
}

// Returns the interface used to reach a public IP (default route).
// Windows tend to pick somehow the wrong interface instead of listening to all/correct
// default one so we try to guess the right one by connecting to an external address.
func GetInternetInterface(target string) (*net.Interface, *net.UDPAddr, error) {
	conn, err := net.Dial("udp4", target) //nolint:noctx // initialization time
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	localIP := localAddr.IP

	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for _, iface := range interfaces {
		want := net.FlagUp | net.FlagMulticast | net.FlagRunning
		if iface.Flags&want != want {
			continue
		}
		// don't want:
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.To4() == nil {
				continue
			}
			if ipnet.IP.Equal(localIP) {
				return &iface, localAddr, nil
			}
		}
	}
	return nil, nil, errors.New("no default route interface found")
}
