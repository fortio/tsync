// Network library support for tsync (discovery/registration and communication).
package tsnet

import "os"

type Config struct {
	// Name to use, if empty hostname will be used.
	Name string
}

type Server struct {
	// Name of this server instance.
	Name string
}

func (c *Config) NewServer() *Server {
	name := c.Name
	return &Server{Name: name}
}

func (s *Server) Start() error {
	if s.Name == "" {
		var err error
		s.Name, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	return nil
}
