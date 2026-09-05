package browserprofile

import (
	"crypto/rand"
	"errors"
	"io"
	"strings"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	metadataVersion = 1
	algorithmName   = "AES-256-GCM"
	kdfName         = "HKDF-SHA-256"
)

var (
	ErrInvalidConfiguration = errors.New("browser profile encryption configuration is invalid")
	ErrIdentityMismatch     = errors.New("browser profile identity does not match")
	ErrKeyGeneration        = errors.New("browser profile root key generation does not match")
	ErrRootKeyUnavailable   = errors.New("browser profile root key is unavailable")
	ErrProfileCorrupt       = errors.New("browser profile is corrupt or cannot be authenticated")
	ErrProfileExists        = errors.New("browser profile already exists")
	ErrProfileNotFound      = errors.New("browser profile does not exist")
	ErrProfileQuarantined   = errors.New("browser profile was quarantined")
	ErrProfileStoreLocked   = errors.New("browser profile store is already in use")
	ErrProfileStoreClosed   = errors.New("browser profile store is closed")
	ErrKeyClosed            = errors.New("browser profile data key is closed")
)

type Identity struct {
	AgentID           string `json:"agent_id"`
	PrincipalScopeID  string `json:"principal_scope_id"`
	ProfileSlot       string `json:"profile_slot"`
	ProfileGeneration uint64 `json:"profile_generation"`
}

type RootKey struct {
	generation uint64
	key        [32]byte
	closed     bool
}

func NewRootKey(generation uint64, raw []byte) (*RootKey, error) {
	if generation == 0 || len(raw) != 32 || allZero(raw) {
		return nil, ErrInvalidConfiguration
	}
	root := &RootKey{generation: generation}
	copy(root.key[:], raw)
	return root, nil
}

func (key *RootKey) Generation() uint64 {
	if key == nil {
		return 0
	}
	return key.generation
}

func (key *RootKey) Close() {
	if key == nil {
		return
	}
	clear(key.key[:])
	key.closed = true
}

type Metadata struct {
	Version           int      `json:"version"`
	ContractID        string   `json:"contract_id"`
	Algorithm         string   `json:"algorithm"`
	KDF               string   `json:"kdf"`
	Identity          Identity `json:"identity"`
	RootKeyGeneration uint64   `json:"root_key_generation"`
	WrapNonce         []byte   `json:"wrap_nonce"`
	WrappedDEK        []byte   `json:"wrapped_dek"`
}

type protector struct {
	random io.Reader
}

func newProtector(randomSource io.Reader) *protector {
	if randomSource == nil {
		randomSource = rand.Reader
	}
	return &protector{random: randomSource}
}

type payloadCipher struct {
	identity Identity
	key      [32]byte
	closed   bool
	random   io.Reader
}

func (cipher *payloadCipher) close() {
	if cipher == nil {
		return
	}
	clear(cipher.key[:])
	cipher.closed = true
}

func (identity Identity) validate() error {
	if !validUUID(identity.AgentID) ||
		!validOpaque(identity.PrincipalScopeID, 256) ||
		!validOpaque(identity.ProfileSlot, 64) ||
		identity.ProfileGeneration == 0 {
		return ErrInvalidConfiguration
	}
	return nil
}

func (identity Identity) equal(other Identity) bool {
	return identity.AgentID == other.AgentID &&
		identity.PrincipalScopeID == other.PrincipalScopeID &&
		identity.ProfileSlot == other.ProfileSlot &&
		identity.ProfileGeneration == other.ProfileGeneration
}

func validUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return value != "00000000-0000-0000-0000-000000000000"
}

func validOpaque(value string, limit int) bool {
	if value == "" || len(value) > limit || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:-", char) {
			continue
		}
		return false
	}
	return true
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}

func contractID() string {
	return browserprotocol.ContractID
}
