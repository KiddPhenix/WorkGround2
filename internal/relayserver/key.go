package relayserver

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const masterKeyFile = "master.key"

// LoadOrCreateMasterKey atomically creates a persistent 256-bit key or loads
// the existing one. The file is deliberately separate from normal config.
func LoadOrCreateMasterKey(dataDir string) ([]byte, error) {
	if dataDir == "" {
		return nil, errors.New("relay data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create relay data directory: %w", err)
	}
	path := filepath.Join(dataDir, masterKeyFile)
	b, err := os.ReadFile(path)
	if err == nil {
		key, decErr := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if decErr != nil || len(key) < 32 {
			return nil, errors.New("relay master key file is invalid")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read relay master key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate relay master key: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".master-key-*")
	if err != nil {
		return nil, fmt.Errorf("create relay master key temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.WriteString(base64.RawStdEncoding.EncodeToString(key) + "\n"); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if existing, loadErr := os.ReadFile(path); loadErr == nil {
			decoded, decErr := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(existing)))
			if decErr == nil && len(decoded) >= 32 {
				return decoded, nil
			}
		}
		return nil, fmt.Errorf("persist relay master key: %w", err)
	}
	return key, nil
}
