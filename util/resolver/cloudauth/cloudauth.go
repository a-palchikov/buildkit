package cloudauth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/containerd/errdefs"

	"github.com/moby/buildkit/util/flightcontrol"
)

// NewAuthorizer creates a new Authorizer value.
func NewAuthorizer(a authorizer) *Authorizer {
	return &Authorizer{a: a}
}

// Authorizer is a generic token authorizer.
// It refreshes tokens on-demand.
// It is safe for use from multiple goroutines.
type Authorizer struct {
	a authorizer
	g flightcontrol.Group[Token]
	// mu protects the fields below
	mu    sync.RWMutex
	token Token
}

// Authorize set the appropriate `Authorize` header on the given request.
func (r *Authorizer) Authorize(ctx context.Context, req *http.Request) error {
	r.mu.RLock()
	if r.token.Valid() {
		if err := r.token.SetAuth(req); err != nil {
			return err
		}
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	token, err := r.g.Do(ctx, r.a.Name(), func(ctx context.Context) (Token, error) {
		r.mu.RLock()
		if r.token.Valid() {
			token := r.token
			r.mu.RUnlock()
			return token, nil
		}
		r.mu.RUnlock()
		return r.a.FetchToken(ctx)
	})
	if err != nil {
		return fmt.Errorf("fetching %s token: %w", r.a.Name(), err)
	}

	r.mu.Lock()
	r.token = token
	r.mu.Unlock()

	return r.token.SetAuth(req)
}

// AddResponses is a no-op.
// Implements docker.Authorizer
func (*Authorizer) AddResponses(ctx context.Context, responses []*http.Response) error {
	return errdefs.ErrNotImplemented
}

// Token is the cached token interface
type Token interface {
	// SetAuth sets the appropriate authorization header on the provided request
	SetAuth(req *http.Request) error
	// Valid determines if the token is still valid.
	// TODO: add more info on the grace period?
	Valid() bool
}

type authorizer interface {
	// Name identifies this authorizer.
	// It does not need to be unique among all existing implementations,
	// but it should be for readability
	Name() string
	// FetchToken fetches the valid registry token
	FetchToken(context.Context) (Token, error)
}
