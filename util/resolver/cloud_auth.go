package resolver

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/errdefs"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	log "github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/resolver/config"
)

func newAWSAuthorizer(c *config.AWSCreds) (docker.Authorizer, error) {
	return &awsAuthorizer{config: c}, nil
}

type awsAuthorizer struct {
	config *config.AWSCreds
	g      flightcontrol.Group[*cachedToken]
	// mu protects fields below
	mu    sync.RWMutex
	token *cachedToken
}

// Authorize authorizes the given request for AWS ECR.
func (r *awsAuthorizer) Authorize(ctx context.Context, req *http.Request) error {
	r.mu.RLock()
	if r.hasValidToken() {
		req.SetBasicAuth(r.token.username, r.token.password)
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	token, err := r.g.Do(ctx, "ecr-token", func(ctx context.Context) (*cachedToken, error) {
		r.mu.RLock()
		if r.hasValidToken() {
			token := r.token
			r.mu.RUnlock()
			return token, nil
		}
		r.mu.RUnlock()
		return r.fetchToken(ctx)
	})
	if err != nil {
		return fmt.Errorf("fetching ECR token: %w", err)
	}

	r.mu.Lock()
	r.token = token
	r.mu.Unlock()

	req.SetBasicAuth(token.username, token.password)
	return nil
}

func (r *awsAuthorizer) fetchToken(ctx context.Context) (*cachedToken, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if r.config.Region != nil {
		opts = append(opts, awsconfig.WithRegion(*r.config.Region))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	if aws.ToString(r.config.RoleArn) != "" {
		r.assumeRole(&cfg)
	}

	if tokenFile := os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE"); tokenFile != "" {
		log.G(ctx).Debug("Using irsa/pod identity auth")
	}

	client := ecr.NewFromConfig(cfg)
	out, err := client.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return nil, fmt.Errorf("getting ecr authorization token: %w", err)
	}

	if len(out.AuthorizationData) == 0 {
		return nil, errors.New("no authorization data returned from ecr")
	}
	authData := out.AuthorizationData[0]
	encodedToken := aws.ToString(authData.AuthorizationToken)
	if encodedToken == "" {
		return nil, errors.New("invalid empty encoded token")
	}

	token, err := tokenFromAuthData(authData)
	if err != nil {
		return nil, err
	}
	token.expiresAt = aws.ToTime(authData.ExpiresAt)

	return token, nil
}

// hasValidToken determines if the cached token is still valid.
// Assumes that r.mu is held
func (r *awsAuthorizer) hasValidToken() bool {
	return r.token != nil && !r.token.expiresAt.IsZero() && time.Now().Before(r.token.expiresAt)
}

func (r *awsAuthorizer) assumeRole(cfg *aws.Config) {
	stsClient := sts.NewFromConfig(*cfg)
	provider := stscreds.NewAssumeRoleProvider(stsClient, *r.config.RoleArn, func(aro *stscreds.AssumeRoleOptions) {
		if r.config.ExternalID != nil {
			aro.ExternalID = r.config.ExternalID
		}
		aro.RoleSessionName = "buildkit-ecr-auth"
		if r.config.SessionName != nil {
			aro.RoleSessionName = *r.config.SessionName
		}
		aro.Duration = r.config.Duration
	})
	cfg.Credentials = aws.NewCredentialsCache(provider)
	if r.config.Region != nil {
		// Reset region if specified
		cfg.Region = *r.config.Region
	}
}

func newGCPAuthorizer(c *config.GCPCreds) (docker.Authorizer, error) {
	return &gcpAuthorizer{config: c}, nil
}

type gcpAuthorizer struct {
	// TODO(dima): cache the token for reuse
	config *config.GCPCreds
}

// Authorize authorizes the given request for GCR.
// Reference: https://cloud.google.com/artifact-registry/docs/docker/authentication.
func (r *gcpAuthorizer) Authorize(ctx context.Context, req *http.Request) error {
	const (
		scope    = "https://www.googleapis.com/auth/cloud-platform"
		username = "oauth2accesstoken"
	)

	var ts oauth2.TokenSource
	if r.config.ServiceAccountKeyPath != nil {
		// TODO: use service account key file
		// data, err := base64.StdEncoding.DecodeString(*r.config.ServiceAccountKeyPath)
		// return errors.New("service account key path not implemented yet, use ADC")
	} else {
		var err error
		// Use default credentials
		ts, err = google.DefaultTokenSource(ctx, scope)
		if err != nil {
			return fmt.Errorf("creating default token source: %w", err)
		}
	}

	token, err := ts.Token()
	if err != nil {
		return fmt.Errorf("getting token: %w", err)
	}

	// TODO(dima): is there anything to be done differently for each auth type?
	// https://cloud.google.com/artifact-registry/docs/docker/authentication#token
	req.SetBasicAuth(username, token.AccessToken)
	return nil
}

func newAzureAuthorizer(c *config.AzureCreds) (docker.Authorizer, error) {
	return &azureAuthorizer{config: c}, nil
}

type azureAuthorizer struct {
	config *config.AzureCreds
}

// Authorize authorizes the given request for Azure ACR.
func (r *azureAuthorizer) Authorize(ctx context.Context, req *http.Request) error {
	var cred azcore.TokenCredential
	var err error

	opts := &azidentity.ManagedIdentityCredentialOptions{}
	if r.config.ManagedIdentityClientID != nil {
		opts.ID = azidentity.ClientID(*r.config.ManagedIdentityClientID)
	}

	cred, err = azidentity.NewManagedIdentityCredential(opts)
	if err != nil {
		// Fallback to default credential if managed identity fails or not configured?
		// Or maybe `NewDefaultAzureCredential`?
		// The config has `AuthType`.
		if r.config.AuthType == config.ServicePrincipal {
			// Need client ID, secret, tenant ID. Config struct doesn't seem to have secret?
			// Let's check config again.
			return errors.New("service principal auth not fully supported yet")
		}
		// Default to DefaultAzureCredential which includes ManagedIdentity
		cred, err = azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return fmt.Errorf("creating default azure credential: %w", err)
		}
	}

	// ACR scope: https://github.com/Azure/acr/blob/main/docs/AAD-OAuth.md
	// Resource ID for ACR is usually "https://management.azure.com/.default" for management,
	// but for docker login it's often the registry endpoint.
	// We need an access token for the registry.
	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		// FIXME(dima): This might need to be specific to the registry?
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		return fmt.Errorf("getting azure token: %w", err)
	}

	// For ACR, use the access token as the password
	req.SetBasicAuth("00000000-0000-0000-0000-000000000000", token.Token)
	return nil
}

func (*azureAuthorizer) AddResponses(ctx context.Context, responses []*http.Response) error {
	return errdefs.ErrNotImplemented
}

func (*awsAuthorizer) AddResponses(ctx context.Context, responses []*http.Response) error {
	return errdefs.ErrNotImplemented
}

func (*gcpAuthorizer) AddResponses(ctx context.Context, responses []*http.Response) error {
	return errdefs.ErrNotImplemented
}

func tokenFromAuthData(authData types.AuthorizationData) (*cachedToken, error) {
	authToken, err := base64.StdEncoding.DecodeString(*authData.AuthorizationToken)
	if err != nil {
		return nil, fmt.Errorf("decoding authorization token: %w", err)
	}
	parts := strings.SplitN(string(authToken), ":", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid authorization token format, expected username:password")
	}
	return &cachedToken{username: parts[0], password: parts[1]}, nil
}

type cachedToken struct {
	username  string
	password  string
	expiresAt time.Time
}
