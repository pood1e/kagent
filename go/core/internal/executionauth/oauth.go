package executionauth

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2"
)

func (s *Service) configureOAuthTLS() error {
	path := s.config.GetOauth().GetCaFile()
	if path == "" {
		return nil
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read OAuth credential CA: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return fmt.Errorf("load OAuth trust roots: %w", err)
	}
	if !roots.AppendCertsFromPEM(pem) {
		return fmt.Errorf("OAuth credential CA contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	s.http.Transport = transport
	return nil
}

// refreshConfig reads rotating client credentials for each OAuth2 exchange.
func (s *Service) refreshConfig() (*oauth2.Config, error) {
	config := s.config.GetOauth()
	if config == nil {
		return nil, fmt.Errorf("renewable execution credentials are not configured")
	}
	secret := ""
	if config.ClientSecretFile != "" {
		data, err := os.ReadFile(config.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("OAuth client credential unavailable")
		}
		secret = strings.TrimSpace(string(data))
	}
	return &oauth2.Config{ClientID: config.ClientId, ClientSecret: secret, Endpoint: oauth2.Endpoint{TokenURL: config.TokenEndpoint, AuthStyle: oauth2.AuthStyleInHeader}}, nil
}
