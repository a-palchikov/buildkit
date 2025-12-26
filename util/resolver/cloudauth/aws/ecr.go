package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/log"

	"github.com/moby/buildkit/util/resolver/cloudauth"
	"github.com/moby/buildkit/util/resolver/config"
)

// New creates a new docker.Authorizer for AWS ECR.
func New(c *config.AWSCreds) docker.Authorizer {
	a := &awsAuthorizer{AWSCreds: c}
	return cloudauth.NewAuthorizer(a)
}

// Name names this authorizer
func (r *awsAuthorizer) Name() string {
	return "ECR"
}

// FetchToken fetches a new ECR token
func (r *awsAuthorizer) FetchToken(ctx context.Context) (cloudauth.Token, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if r.Region != nil {
		opts = append(opts, awsconfig.WithRegion(*r.Region))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}

	if aws.ToString(r.RoleArn) != "" {
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

type awsAuthorizer struct {
	*config.AWSCreds
}

func (r *awsAuthorizer) assumeRole(cfg *aws.Config) {
	stsClient := sts.NewFromConfig(*cfg)
	provider := stscreds.NewAssumeRoleProvider(stsClient, *r.RoleArn, func(aro *stscreds.AssumeRoleOptions) {
		if r.ExternalID != nil {
			aro.ExternalID = r.ExternalID
		}
		aro.RoleSessionName = "buildkit-ecr-auth"
		if r.SessionName != nil {
			aro.RoleSessionName = *r.SessionName
		}
		aro.Duration = r.Duration
	})
	cfg.Credentials = aws.NewCredentialsCache(provider)
	if r.Region != nil {
		// Reset region if specified
		cfg.Region = *r.Region
	}
}

func tokenFromAuthData(authData types.AuthorizationData) (*token, error) {
	authToken, err := base64.StdEncoding.DecodeString(*authData.AuthorizationToken)
	if err != nil {
		return nil, fmt.Errorf("decoding authorization token: %w", err)
	}
	parts := strings.SplitN(string(authToken), ":", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid authorization token format, expected username:password")
	}
	return &token{username: parts[0], password: parts[1]}, nil
}

// Valid determines if this token is still valid.
// Implements cloudauth.Token
func (r *token) Valid() bool {
	return r != nil && !r.expiresAt.IsZero() && time.Now().Before(r.expiresAt)
}

// SetAuth sets the appropriate `Authorization` header on the given request
func (r *token) SetAuth(req *http.Request) error {
	req.SetBasicAuth(r.username, r.password)
	return nil
}

type token struct {
	username  string
	password  string
	expiresAt time.Time
}
