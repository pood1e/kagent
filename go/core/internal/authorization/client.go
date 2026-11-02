package authorization

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"buf.build/go/protovalidate"
	fga "github.com/openfga/go-sdk"
	fgaclient "github.com/openfga/go-sdk/client"
	"github.com/openfga/go-sdk/credentials"

	configv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/config/v1"
	"github.com/kagent-dev/kagent/go/core/pkg/config"
)

func newClient(cfg *configv1.OpenFGAConfig) (*fgaclient.OpenFgaClient, *http.Transport, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("resource authorization configuration is required")
	}
	if err := protovalidate.Validate(cfg); err != nil {
		return nil, nil, fmt.Errorf("invalid resource authorization configuration: %w", err)
	}
	endpoint, err := url.Parse(cfg.ApiUrl)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid authorization endpoint: %w", err)
	}
	if endpoint.Hostname() == "" {
		return nil, nil, fmt.Errorf("authorization endpoint requires a hostname")
	}
	loopback := endpoint.Hostname() == "localhost" || net.ParseIP(endpoint.Hostname()).IsLoopback()
	if endpoint.Scheme != "https" && !loopback {
		return nil, nil, fmt.Errorf("authorization endpoint requires HTTPS outside loopback")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CaFile != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, nil, fmt.Errorf("load authorization system CA roots: %w", err)
		}
		pem, err := os.ReadFile(cfg.CaFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read authorization CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("authorization CA file contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	var credential *credentials.Credentials
	if cfg.ApiTokenFile != "" {
		token, err := config.ReadSecret(cfg.ApiTokenFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read authorization credential: %w", err)
		}
		credential = &credentials.Credentials{
			Method: credentials.CredentialsMethodApiToken,
			Config: &credentials.Config{ApiToken: token},
		}
	} else if !loopback {
		return nil, nil, fmt.Errorf("authorization service credential is required")
	}
	sdk, err := fgaclient.NewSdkClient(&fgaclient.ClientConfiguration{
		ApiUrl: cfg.ApiUrl, StoreId: cfg.StoreId, AuthorizationModelId: cfg.AuthorizationModelId,
		Credentials: credential,
		RetryParams: &fga.RetryParams{MaxRetry: 0, MinWaitInMs: 100},
		HTTPClient: &http.Client{
			Transport: transport, Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, fmt.Errorf("configure authorization client: %w", err)
	}
	return sdk, transport, nil
}
