package gcp

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/moby/buildkit/util/resolver/cloudauth"
	"github.com/moby/buildkit/util/resolver/config"
)

// New creates a new Google Artifact Registry authorizer value.
func New(c *config.GCPCreds) docker.Authorizer {
	a := &authorizer{GCPCreds: c}
	return cloudauth.NewAuthorizer(a)
}

// Name names this authorizer.
func (r *authorizer) Name() string {
	return "Google Artifact Registry"
}

// FetchToken fetches an authorization token for Google Artifact Registry.
// Reference: https://cloud.google.com/artifact-registry/docs/docker/authentication.
func (r *authorizer) FetchToken(ctx context.Context) (cloudauth.Token, error) {
	var ts oauth2.TokenSource
	if r.ServiceAccountKeyPath != nil {
		// TODO: use service account key file
		// data, err := base64.StdEncoding.DecodeString(*r.config.ServiceAccountKeyPath)
		// return errors.New("service account key path not implemented yet, use ADC")
	} else {
		var err error
		// Use default application credentials
		ts, err = google.DefaultTokenSource(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("creating default token source: %w", err)
		}
	}

	oauthToken, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("getting token: %w", err)
	}

	return &token{token: oauthToken}, nil
}

type authorizer struct {
	*config.GCPCreds
}

// SetAuth sets the appropriate `Authorization` header.
// Implements cloudauth.Token
func (r *accountKey) SetAuth(req *http.Request) error {
	bytes, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("reading account key file %q: %w", r.path, err)
	}
	req.SetBasicAuth("_json_key", string(bytes))
	return nil
}

// Valid determines if this token is valid.
// Implements cloudauth.Token
func (r *accountKey) Valid() bool {
	// Service account key is always valid
	return true
}

type accountKey struct {
	// path reference the account key file. It is read on demand to avoid
	// keeping the key contents in memory
	path string
}

// SetAuth sets the appropriate `Authorization` header.
// Implements cloudauth.Token
func (r *token) SetAuth(req *http.Request) error {
	// TODO(dima): is there anything to be done differently for each auth type?
	// https://cloud.google.com/artifact-registry/docs/docker/authentication#token
	req.SetBasicAuth(username, r.token.AccessToken)
	return nil
}

// Valid determines if this token is valid.
// Implements cloudauth.Token
func (r *token) Valid() bool {
	return r.token.Valid()
}

type token struct {
	token *oauth2.Token
}

const (
	scope    = "https://www.googleapis.com/auth/cloud-platform"
	username = "oauth2accesstoken"
)
