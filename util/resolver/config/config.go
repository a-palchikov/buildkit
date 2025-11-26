package config

import "time"

type RegistryConfig struct {
	Mirrors      []string     `toml:"mirrors"`
	PlainHTTP    *bool        `toml:"http"`
	Insecure     *bool        `toml:"insecure"`
	RootCAs      []string     `toml:"ca"`
	KeyPairs     []TLSKeyPair `toml:"keypair"`
	TLSConfigDir []string     `toml:"tlsconfigdir"`

	// Cloud-specific authentication configuration
	AWS   *AWSCreds   `toml:"aws"`
	GCP   *GCPCreds   `toml:"gcp"`
	Azure *AzureCreds `toml:"azure"`
}

// AWSCreds defines the credential configuration for AWS ECR
type AWSCreds struct {
	// AuthType specifies the underlying mechanism
	AuthType AWSAuthType `toml:"auth-type"`

	// RoleArn is the optional IAM role to assume.
	// If "none", explicitly does not assume any role.
	// If unspecified, use the default SDK's behavior.
	RoleArn *string `toml:"assume-role-arn,omitempty"`

	// ExternalID is an optional security token used when assuming a role.
	// Required when the trust policy for RoleArn includes a sts:ExternalId condition.
	// See: https://docs.aws.amazon.com/IAM/latest/UserGuide/confused-deputy.html
	ExternalID *string `toml:"external-id,omitempty"`

	// Region is the optional target ECR region.
	// Can be extracted from the registry host pattern (*.{region}.amazonaws.com)
	Region *string `toml:"region,omitempty"`

	// SessionName is an optional identifier for the assumed role session.
	// Useful for CloudTrail auditing.
	SessionName *string `toml:"session-name,omitempty"`

	// Duration optionally limits the STS credentials.
	Duration time.Duration `toml:"duration,omitzero"`
}

// GCPCreds defines the credential configuration for GCP AR
type GCPCreds struct {
	// AuthType specifies the identity source: "adc" (default), "workload-identity", or "service-account".
	AuthType GCPAuthType `toml:"auth-type"`

	// KeyPath is the optional path to a service account key file.
	ServiceAccountKeyPath *string `toml:"service-account-key-path,omitempty"`

	// Audience is the expected recipient of the token, used when exchanging a
	// Workload Identity token for a Google-issued token.
	Audience *string `toml:"audience,omitempty"`
}

// AzureCreds de
type AzureCreds struct {
	// AuthType specifies the identity source: "managed-identity" or "service-principal".
	AuthType AzureAuthType `toml:"auth-type"`

	// ManagedIdentityClientID specifies the Client ID of the Managed Identity
	// to use for token acquisition. This ID ensures the correct identity
	// is targeted when multiple identities are assigned to the host.
	// If unspecified, defaults to the system-assigned identity.
	ManagedIdentityClientID *string `toml:"managed-identity-client-id,omitempty"`

	// TenantID optionally specifies the Azure Active Directory tenant housing the Service Principal.
	TenantID *string `toml:"tenant-id,omitempty"`
}

// AWSAuthType defines supported authentication types for AWS ECR
type AWSAuthType string

const (
	// IRSA indicates the use of IAM roles for service accounts (kubernetes)
	IRSAAuth AWSAuthType = "irsa"
)

// GCPAuthType defines supported authentication types for GCP AR
type GCPAuthType string

const (
	ADC              AWSAuthType = "adc"
	WorkloadIdentity GCPAuthType = "workload-identity"
	ServiceAccount   GCPAuthType = "service-account"
)

// AzureuthType defines supported authentication types for Azure ACR
type AzureAuthType string

const (
	ManagedIdentity  AzureAuthType = "managed-identity"
	ServicePrincipal AzureAuthType = "service-principal"
)

type TLSKeyPair struct {
	Key         string `toml:"key"`
	Certificate string `toml:"cert"`
}
