package executionauth

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
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

const jwtBearerGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// grant reads rotating client credentials and signs one assertion for each
// exchange, with the token endpoint as its audience (RFC 7523 section 3).
func (s *Service) grant(subject string) (*clientcredentials.Config, error) {
	config := s.config.GetOauth()
	if config == nil || s.assertions == nil {
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
	assertion, err := s.assertions.sign(subject, config.TokenEndpoint)
	if err != nil {
		return nil, fmt.Errorf("sign delegation assertion")
	}
	return &clientcredentials.Config{
		ClientID: config.ClientId, ClientSecret: secret, TokenURL: config.TokenEndpoint, AuthStyle: oauth2.AuthStyleInHeader,
		// clientcredentials permits replacing grant_type for extension grants.
		EndpointParams: url.Values{"grant_type": {jwtBearerGrant}, "assertion": {assertion}},
	}, nil
}
