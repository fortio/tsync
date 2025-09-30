// Network library support for tsync (discovery/registration and communication).
package tsnet

import (
	"context"
	"os"
	"sync"
	"time"

	"fortio.org/log"
)

type Config struct {
	// Name to use, if empty hostname will be used.
	Name string
}

type Server struct {
	// Name of this server instance.
	Name   string
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (c *Config) NewServer() *Server {
	name := c.Name
	return &Server{Name: name}
}

func (s *Server) Start(ctx context.Context) error {
	if s.Name == "" {
		var err error
		s.Name, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	// get a cancelable context
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go s.run(ctx)
	return nil
}

func (s *Server) Stop() error {
	s.cancel()
	s.wg.Wait()
	return nil
}

func (s *Server) run(ctx context.Context) {
	defer s.wg.Done()
	// 1 sec tick
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	epoch := 0
	for {
		select {
		case <-ctx.Done():
			log.Infof("Exiting tsync server %q", s.Name)
			return
		case <-ticker.C:
			epoch++
			log.Infof("Tick %d", epoch)
		}
	}
}
