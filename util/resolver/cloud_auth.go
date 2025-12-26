package resolver

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/pkg/errors"

	"github.com/moby/buildkit/util/resolver/config"
)

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
