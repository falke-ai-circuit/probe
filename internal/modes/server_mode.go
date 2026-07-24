package modes

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/server"
)

// ServerMode wraps the PROBE server as a startable/stoppable mode.
type ServerMode struct {
	mu       sync.Mutex
	running  bool
	srv      *server.Server
	opts     ServerOptions
	stopCh   chan struct{}
}

// ServerOptions holds the configuration for the server mode.
type ServerOptions struct {
	Addr            string
	Token           string
	RegistryPath    string
	RateLimit       float64
	RateBurst       int
	MaxConcurrent   int
	TokenTTL        time.Duration
	CertFile        string
	KeyFile         string
	ClientCA        string
	ExtraTokens     string
	RequireAPIAuth  bool
	Proxy           string
	EnrollmentPath  string
	CADir           string
	BuilderPath     string
	BuilderOutputDir string
	ProfilesPath    string
	AllowedCIDR     string
	AdminPassword   string
	OperatorPath    string
	VTAPIKey        string
	Version         string
}

// NewServerMode creates a new server mode with the given options.
func NewServerMode(opts ServerOptions) *ServerMode {
	return &ServerMode{
		opts:   opts,
		stopCh: make(chan struct{}),
	}
}

func (s *ServerMode) Name() string { return "serve" }

func (s *ServerMode) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}

	rlCfg := server.RateLimitConfig{
		RatePerSec:    s.opts.RateLimit,
		Burst:         s.opts.RateBurst,
		MaxConcurrent: s.opts.MaxConcurrent,
	}

	useTLS := s.opts.CertFile != "" && s.opts.KeyFile != ""
	if useTLS {
		s.srv = server.NewServerWithTLSRateLimit(s.opts.Addr, s.opts.Token, s.opts.RegistryPath,
			s.opts.CertFile, s.opts.KeyFile, s.opts.ClientCA, rlCfg)
	} else {
		s.srv = server.NewServerWithRateLimit(s.opts.Addr, s.opts.Token, s.opts.RegistryPath, rlCfg)
	}

	s.srv.SetTokenTTL(s.opts.TokenTTL)
	s.srv.SetVersion(s.opts.Version)
	s.srv.SetRequireAPIAuth(s.opts.RequireAPIAuth)
	s.srv.SetEnrollmentPath(s.opts.EnrollmentPath)
	s.srv.SetCADir(s.opts.CADir)
	s.srv.SetBuilderPath(s.opts.BuilderPath, s.opts.BuilderOutputDir)
	s.srv.SetProfilesPath(s.opts.ProfilesPath)
	s.srv.SetOperatorPath(s.opts.OperatorPath)
	s.srv.SetAllowedCIDR(s.opts.AllowedCIDR)

	if s.opts.VTAPIKey != "" {
		s.srv.SetVTAPIKey(s.opts.VTAPIKey)
	}

	if s.opts.AdminPassword != "" && s.srv.Operators().IsEmpty() {
		op, err := s.srv.Operators().CreateWithPassword("admin", "admin", s.opts.AdminPassword, "")
		if err != nil {
			log.Printf("[serve] WARNING: failed to create default admin operator: %v", err)
		} else {
			log.Printf("[serve] Created default admin operator (id=%s)", op.ID)
		}
	}

	// Configure reverse proxies
	var proxies []server.ProxyEntry
	if s.opts.Proxy != "" {
		for _, p := range strings.Split(s.opts.Proxy, ",") {
			parts := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(parts) == 2 {
				proxies = append(proxies, server.ProxyEntry{
					PathPrefix: strings.TrimSpace(parts[0]),
					TargetURL:  strings.TrimSpace(parts[1]),
				})
			}
		}
	} else {
		proxies = []server.ProxyEntry{
			{PathPrefix: "/logreport", TargetURL: "http://localhost:8642"},
		}
	}
	s.srv.SetProxies(proxies)

	if s.opts.ExtraTokens != "" {
		extra := strings.Split(s.opts.ExtraTokens, ",")
		for i, t := range extra {
			extra[i] = strings.TrimSpace(t)
		}
		s.srv.SetExtraTokens(extra)
	}

	s.running = true

	go func() {
		if useTLS {
			if err := s.srv.StartTLS("", ""); err != nil {
				log.Printf("[serve] server error: %v", err)
			}
		} else {
			if err := s.srv.Start(); err != nil {
				log.Printf("[serve] server error: %v", err)
			}
		}
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	return nil
}

func (s *ServerMode) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	s.running = false
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

func (s *ServerMode) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}