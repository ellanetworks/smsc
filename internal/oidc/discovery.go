package oidc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
)

const (
	httpTimeout  = 10 * time.Second
	maxJWKSBytes = 1 << 20
)

var ErrProvider = errors.New("the identity provider")

var signingAlgorithms = []string{
	gooidc.RS256, gooidc.RS384, gooidc.RS512, gooidc.ES256, gooidc.ES384, gooidc.ES512,
	gooidc.PS256, gooidc.PS384, gooidc.PS512, gooidc.EdDSA,
}

type Discovery struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	JWKSURI               string
	SigningAlgorithms     []string
	Keys                  int
	PKCE                  bool
	IssuerParameter       bool
}

// OpenID Connect Discovery 1.0 section 3
type metadata struct {
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ResponseTypes         []string `json:"response_types_supported"`
	SigningAlgorithms     []string `json:"id_token_signing_alg_values_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	IssuerParameter       bool     `json:"authorization_response_iss_parameter_supported"`
}

func HTTPClient(caCertificate string) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("unexpected default HTTP transport")
	}

	transport = transport.Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	if caCertificate != "" {
		pool, err := certPool(caCertificate)
		if err != nil {
			return nil, err
		}

		transport.TLSClientConfig.RootCAs = pool
	}

	return &http.Client{Transport: transport, Timeout: httpTimeout}, nil
}

// OpenID Connect Discovery 1.0 section 4.3
func Discover(ctx context.Context, issuer, caCertificate string) (Discovery, error) {
	if err := errors.Join(ValidateIssuer(issuer), ValidateCACertificate(caCertificate)); err != nil {
		return Discovery{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	client, err := HTTPClient(caCertificate)
	if err != nil {
		return Discovery{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	_, d, err := discover(ctx, client, issuer)
	if err != nil {
		return Discovery{}, err
	}

	if d.Keys, err = countSigningKeys(ctx, client, d.JWKSURI); err != nil {
		return Discovery{}, fmt.Errorf("%w: keys: %w", ErrProvider, err)
	}

	return d, nil
}

func discover(ctx context.Context, client *http.Client, issuer string) (*gooidc.Provider, Discovery, error) {
	provider, err := gooidc.NewProvider(gooidc.ClientContext(ctx, client), issuer)
	if err != nil {
		return nil, Discovery{}, fmt.Errorf("%w: discovery: %w", ErrProvider, err)
	}

	var m metadata
	if err := provider.Claims(&m); err != nil {
		return nil, Discovery{}, fmt.Errorf("%w: discovery: %w", ErrProvider, err)
	}

	d, err := m.check(issuer)
	if err != nil {
		return nil, Discovery{}, fmt.Errorf("%w: discovery: %w", ErrProvider, err)
	}

	return provider, d, nil
}

func (m metadata) check(issuer string) (Discovery, error) {
	for name, endpoint := range map[string]string{
		"authorization_endpoint": m.AuthorizationEndpoint,
		"token_endpoint":         m.TokenEndpoint,
		"jwks_uri":               m.JWKSURI,
	} {
		if u, err := url.Parse(endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
			return Discovery{}, fmt.Errorf("%s must be an https URL", name)
		}
	}

	if !slices.Contains(m.ResponseTypes, "code") {
		return Discovery{}, errors.New("the provider does not support the authorization code flow")
	}

	advertised := m.SigningAlgorithms
	if len(advertised) == 0 {
		advertised = []string{gooidc.RS256}
	}

	var algorithms []string

	for _, alg := range advertised {
		if slices.Contains(signingAlgorithms, alg) {
			algorithms = append(algorithms, alg)
		}
	}

	if len(algorithms) == 0 {
		return Discovery{}, fmt.Errorf("the provider signs ID tokens with %v, and none is an asymmetric algorithm", advertised)
	}

	return Discovery{
		Issuer:                issuer,
		AuthorizationEndpoint: m.AuthorizationEndpoint,
		TokenEndpoint:         m.TokenEndpoint,
		JWKSURI:               m.JWKSURI,
		SigningAlgorithms:     algorithms,
		PKCE:                  slices.Contains(m.CodeChallengeMethods, "S256"),
		IssuerParameter:       m.IssuerParameter,
	}, nil
}

// RFC 7517 section 4.2
func countSigningKeys(ctx context.Context, client *http.Client, jwksURI string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s returned %s", jwksURI, resp.Status)
	}

	var set jose.JSONWebKeySet
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return 0, fmt.Errorf("%s: %w", jwksURI, err)
	}

	n := 0

	for _, k := range set.Keys {
		if (k.Use == "" || k.Use == "sig") && k.IsPublic() && k.Valid() {
			n++
		}
	}

	if n == 0 {
		return 0, fmt.Errorf("%s has no public signing key", jwksURI)
	}

	return n, nil
}
