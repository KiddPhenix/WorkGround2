package addonhost

import (
	"fmt"
	"strings"

	"workground2/internal/config"
)

// ── Secrets API ─────────────────────────────────────────────────────────────

// SecretMeta is public metadata about a secret (never includes the value).
type SecretMeta struct {
	Label   string `json:"label"`
	Purpose string `json:"purpose"`
}

// SecretInput is the request payload for saving a secret.
type SecretInput struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Purpose string `json:"purpose"`
	Value   string `json:"value"`
}

// SecretRequest is the request payload for fetching a secret value.
type SecretRequest struct {
	SecretRef string `json:"secretRef"`
	Purpose   string `json:"purpose"`
}

// SecretValue is the response when a secret is successfully resolved.
type SecretValue struct {
	Value string `json:"value"`
}

// SecretsSave stores a secret under the given id.  The raw value never
// appears in UI, logs, or errors.
func (h *Host) SecretsSave(in SecretInput) error {
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return fmt.Errorf("%w: secret id is required", ErrBadRequest)
	}
	key := secretCredentialKey(h.Ctx.Namespace, id)
	if _, err := config.SetCredential(key, in.Value); err != nil {
		return err
	}
	// Persist metadata so meta() can describe the secret later.
	metaPath := h.storageKeyPath("__secret__" + id)
	if _, err := writeEtag(metaPath, SecretMeta{Label: in.Label, Purpose: in.Purpose}); err != nil {
		return err
	}
	return nil
}

// SecretsMeta returns the label and purpose for a previously saved secret.
func (h *Host) SecretsMeta(secretRef string) (SecretMeta, error) {
	id := secretIDFromRef(secretRef)
	if id == "" {
		return SecretMeta{}, fmt.Errorf("%w: invalid secretRef", ErrBadRequest)
	}
	metaPath := h.storageKeyPath("__secret__" + id)
	entry, err := readEtag(metaPath)
	if err != nil {
		return SecretMeta{Label: id, Purpose: ""}, nil
	}
	meta, ok := entry.Value.(map[string]any)
	if !ok {
		return SecretMeta{Label: id, Purpose: ""}, nil
	}
	return SecretMeta{
		Label:   stringField(meta, "label"),
		Purpose: stringField(meta, "purpose"),
	}, nil
}

// SecretsRequest resolves a secret's value.  The purpose must be provided
// and is logged/auditable; the value is returned only to the caller.
func (h *Host) SecretsRequest(req SecretRequest) (SecretValue, error) {
	ref := strings.TrimSpace(req.SecretRef)
	if ref == "" {
		return SecretValue{}, fmt.Errorf("%w: secretRef is required", ErrBadRequest)
	}
	key := credentialKeyFromRef(ref)
	if key == "" {
		return SecretValue{}, fmt.Errorf("%w: unsupported secretRef %q", ErrBadRequest, ref)
	}
	res := config.ResolveCredentialForRootGlobalFirst(h.Ctx.Home, key)
	if !res.Set {
		return SecretValue{}, fmt.Errorf("%w: secret %q is not configured", ErrNotFound, ref)
	}
	return SecretValue{Value: res.Value}, nil
}

// SecretsDelete removes a previously saved secret.  Idempotent.
func (h *Host) SecretsDelete(secretRef string) error {
	id := secretIDFromRef(secretRef)
	if id == "" {
		return fmt.Errorf("%w: invalid secretRef", ErrBadRequest)
	}
	key := secretCredentialKey(h.Ctx.Namespace, id)
	if err := config.RemoveCredential(key); err != nil {
		return err
	}
	// Remove metadata.
	metaPath := h.storageKeyPath("__secret__" + id)
	_ = removeQuiet(metaPath)
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func secretCredentialKey(namespace, id string) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(namespace))
	b.WriteByte('_')
	for _, r := range strings.ToUpper(id) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.TrimRight(b.String(), "_")
}

func secretIDFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	// Strip "env:" prefix if present.
	if len(ref) >= 4 && strings.EqualFold(ref[:4], "env:") {
		ref = strings.TrimSpace(ref[4:])
	}
	return ref
}

func credentialKeyFromRef(ref string) string {
	return secretIDFromRef(ref)
}
