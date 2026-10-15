package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/FlyTOmeLight/server-go/db"

	"github.com/FlyTOmeLight/artifact-go/internal/storage"
	"github.com/FlyTOmeLight/artifact-go/internal/storagehandoff"
)

// StorageHandoffConfig connects artifact to the authenticated storage listener.
// HTTP is permitted only for loopback development; other endpoints require TLS.
type StorageHandoffConfig struct{ Endpoint, Token string }

// StorageScopeConfig grants one exact tenant or project, never a wildcard.
type StorageScopeConfig struct {
	TenantID  string
	ProjectID *string
}

// StorageConsumerConfig permits credential rotation without changing reference ownership.
type StorageConsumerConfig struct {
	ID     string
	Tokens []string
	Scopes []StorageScopeConfig
}

// StorageInternalConfig owns an independent, service-authenticated HTTP listener.
// A certificate and key are required for non-loopback addresses.
type StorageInternalConfig struct {
	ListenAddress     string
	CertFile, KeyFile string
	Consumers         []StorageConsumerConfig
}

func (c *StorageHandoffConfig) clientConfig() storagehandoff.ClientConfig {
	return storagehandoff.ClientConfig{Endpoint: c.Endpoint, Token: c.Token}
}

func (c *StorageInternalConfig) consumers() []storagehandoff.Consumer {
	out := make([]storagehandoff.Consumer, len(c.Consumers))
	for i, consumer := range c.Consumers {
		out[i] = storagehandoff.Consumer{ID: consumer.ID, Tokens: append([]string(nil), consumer.Tokens...)}
		for _, scope := range consumer.Scopes {
			var project *string
			if scope.ProjectID != nil {
				copy := *scope.ProjectID
				project = &copy
			}
			out[i].Scopes = append(
				out[i].Scopes,
				storagehandoff.Scope{TenantID: scope.TenantID, ProjectID: project},
			)
		}
	}
	return out
}

func (c *StorageInternalConfig) tlsConfig() (*tls.Config, error) {
	host, _, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return nil, errors.New("storage internal listener requires host:port")
	}
	if c.CertFile == "" && c.KeyFile == "" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New(
				"storage internal HTTP requires a literal loopback address; otherwise configure TLS",
			)
		}
		return nil, nil
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, errors.New("storage internal TLS requires both certificate and key")
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, errors.New("storage internal TLS certificate could not be loaded")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}

type storageInternalServer struct {
	address   string
	tlsConfig *tls.Config
	server    *http.Server
	done      chan struct{}
	serveErr  error
	requestMu sync.Mutex
	draining  bool
	requests  sync.WaitGroup
}

func newStorageInternalServer(database *db.DB, c *StorageInternalConfig) (*storageInternalServer, error) {
	if c == nil {
		return nil, nil
	}
	tlsConfig, err := c.tlsConfig()
	if err != nil {
		return nil, err
	}
	handler, err := storagehandoff.NewHandler(storage.NewBindingService(database), c.consumers())
	if err != nil {
		return nil, err
	}
	s := &storageInternalServer{address: c.ListenAddress, tlsConfig: tlsConfig}
	s.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestMu.Lock()
		if s.draining {
			s.requestMu.Unlock()
			http.Error(w, "storage is shutting down", http.StatusServiceUnavailable)
			return
		}
		s.requests.Add(1)
		s.requestMu.Unlock()
		defer s.requests.Done()
		handler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: time.Minute}
	return s, nil
}

// start binds before the public host starts accepting business requests.
func (s *storageInternalServer) start(ctx context.Context) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", s.address)
	if err != nil {
		return err
	}
	if s.tlsConfig != nil {
		listener = tls.NewListener(listener, s.tlsConfig)
	}
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		err := s.server.Serve(listener)
		if !errors.Is(err, http.ErrServerClosed) {
			s.serveErr = err
		}
	}()
	return nil
}

func (s *storageInternalServer) stop(ctx context.Context) error {
	if s.done == nil {
		return nil
	}
	s.requestMu.Lock()
	s.draining = true
	s.requestMu.Unlock()
	err := s.server.Shutdown(ctx)
	if err != nil {
		_ = s.server.Close()
		err = fmt.Errorf("stop storage internal listener: %w", err)
	}
	<-s.done
	// Closing sockets cancels requests but does not join their database work.
	s.requests.Wait()
	return errors.Join(err, s.serveErr)
}
