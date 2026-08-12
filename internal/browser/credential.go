package browser

import "context"

// CredentialRequest describes a request for stored credentials.
type CredentialRequest struct {
	OwnerID      string
	Origin       string
	Reference    string
	UsernameHint string
	Reason       string
}

// CredentialLease provides temporary access to a credential's secret.
// Implementations must zero the buffer after WithSecret returns.
type CredentialLease interface {
	Username() string
	WithSecret(func([]byte) error) error
	Close() error
}

// CredentialProvider acquires credentials from a secure store.
type CredentialProvider interface {
	Acquire(ctx context.Context, req CredentialRequest) (CredentialLease, error)
}

// SecureTyper is an optional Driver capability for typing secrets
// without exposing them to logs or tool transcripts.
type SecureTyper interface {
	TypeSecret(ctx context.Context, ref NodeRef, secret []byte) error
}

// disabledCredentialProvider is the default V1 provider: always disabled.
type disabledCredentialProvider struct{}

// NewDisabledCredentialProvider returns a provider that always returns disabled.
func NewDisabledCredentialProvider() CredentialProvider {
	return disabledCredentialProvider{}
}

func (disabledCredentialProvider) Acquire(_ context.Context, _ CredentialRequest) (CredentialLease, error) {
	return nil, NewError(ErrCredentialProviderDisabled, "credential provider is not enabled in V1", nil)
}
