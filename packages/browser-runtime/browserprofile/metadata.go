package browserprofile

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const wrappingKeyContext = "openlinker.browser.profile.wrap.v1"

const maxMetadataBytes = 4096

func marshalMetadata(metadata Metadata) ([]byte, error) {
	if validateMetadataShape(metadata) != nil {
		return nil, ErrProfileCorrupt
	}
	raw, err := json.Marshal(metadata)
	if err != nil || len(raw) > maxMetadataBytes {
		return nil, ErrProfileCorrupt
	}
	return raw, nil
}

func parseMetadata(raw []byte) (Metadata, error) {
	if len(raw) == 0 || len(raw) > maxMetadataBytes {
		return Metadata{}, ErrProfileCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var metadata Metadata
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, ErrProfileCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Metadata{}, ErrProfileCorrupt
	}
	if validateMetadataShape(metadata) != nil {
		return Metadata{}, ErrProfileCorrupt
	}
	return metadata, nil
}

func (protector *protector) create(
	identity Identity,
	root *RootKey,
) (Metadata, *payloadCipher, error) {
	if protector == nil || protector.random == nil || identity.validate() != nil || !validRoot(root) {
		return Metadata{}, nil, ErrInvalidConfiguration
	}
	var dek [32]byte
	if _, err := io.ReadFull(protector.random, dek[:]); err != nil {
		return Metadata{}, nil, fmt.Errorf("generate browser profile data key: %w", err)
	}
	metadata, err := protector.wrap(identity, root, dek[:])
	if err != nil {
		clear(dek[:])
		return Metadata{}, nil, err
	}
	payloadCipher := &payloadCipher{identity: identity, random: protector.random}
	copy(payloadCipher.key[:], dek[:])
	clear(dek[:])
	return metadata, payloadCipher, nil
}

func (protector *protector) open(
	metadata Metadata,
	expected Identity,
	root *RootKey,
) (*payloadCipher, error) {
	if protector == nil || protector.random == nil || expected.validate() != nil || !validRoot(root) {
		return nil, ErrInvalidConfiguration
	}
	dek, err := unwrap(metadata, expected, root)
	if err != nil {
		return nil, err
	}
	payloadCipher := &payloadCipher{identity: expected, random: protector.random}
	copy(payloadCipher.key[:], dek)
	clear(dek)
	return payloadCipher, nil
}

func (protector *protector) rewrap(
	metadata Metadata,
	expected Identity,
	oldRoot *RootKey,
	newRoot *RootKey,
) (Metadata, error) {
	if protector == nil || protector.random == nil || !validRoot(newRoot) ||
		!validRoot(oldRoot) || newRoot.generation <= oldRoot.generation {
		return Metadata{}, ErrInvalidConfiguration
	}
	dek, err := unwrap(metadata, expected, oldRoot)
	if err != nil {
		return Metadata{}, err
	}
	defer clear(dek)
	return protector.wrap(expected, newRoot, dek)
}

func (protector *protector) wrap(
	identity Identity,
	root *RootKey,
	dek []byte,
) (Metadata, error) {
	wrappingKey, err := deriveWrappingKey(identity, root)
	if err != nil {
		return Metadata{}, err
	}
	defer clear(wrappingKey)
	gcm, err := newGCM(wrappingKey)
	if err != nil {
		return Metadata{}, ErrInvalidConfiguration
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(protector.random, nonce); err != nil {
		return Metadata{}, fmt.Errorf("generate browser profile wrap nonce: %w", err)
	}
	metadata := Metadata{
		Version:           metadataVersion,
		ContractID:        contractID(),
		Algorithm:         algorithmName,
		KDF:               kdfName,
		Identity:          identity,
		RootKeyGeneration: root.generation,
		WrapNonce:         nonce,
	}
	metadata.WrappedDEK = gcm.Seal(nil, nonce, dek, wrapAAD(metadata))
	return metadata, nil
}

func unwrap(metadata Metadata, expected Identity, root *RootKey) ([]byte, error) {
	if expected.validate() != nil || !validRoot(root) {
		return nil, ErrInvalidConfiguration
	}
	if validateMetadataShape(metadata) != nil {
		return nil, ErrProfileCorrupt
	}
	if !metadata.Identity.equal(expected) {
		return nil, ErrIdentityMismatch
	}
	if metadata.RootKeyGeneration != root.generation {
		return nil, ErrKeyGeneration
	}
	wrappingKey, err := deriveWrappingKey(expected, root)
	if err != nil {
		return nil, err
	}
	defer clear(wrappingKey)
	gcm, err := newGCM(wrappingKey)
	if err != nil || len(metadata.WrapNonce) != gcm.NonceSize() {
		return nil, ErrProfileCorrupt
	}
	dek, err := gcm.Open(nil, metadata.WrapNonce, metadata.WrappedDEK, wrapAAD(metadata))
	if err != nil || len(dek) != 32 {
		clear(dek)
		return nil, ErrProfileCorrupt
	}
	return dek, nil
}

func deriveWrappingKey(identity Identity, root *RootKey) ([]byte, error) {
	context, err := json.Marshal(struct {
		ContractID        string   `json:"contract_id"`
		Purpose           string   `json:"purpose"`
		Identity          Identity `json:"identity"`
		RootKeyGeneration uint64   `json:"root_key_generation"`
	}{
		ContractID:        contractID(),
		Purpose:           "profile-dek-wrap",
		Identity:          identity,
		RootKeyGeneration: root.generation,
	})
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	return hkdf.Key(sha256.New, root.key[:], []byte(wrappingKeyContext), string(context), 32)
}

func wrapAAD(metadata Metadata) []byte {
	raw, _ := json.Marshal(struct {
		Version           int      `json:"version"`
		ContractID        string   `json:"contract_id"`
		Algorithm         string   `json:"algorithm"`
		KDF               string   `json:"kdf"`
		Identity          Identity `json:"identity"`
		RootKeyGeneration uint64   `json:"root_key_generation"`
	}{
		Version:           metadata.Version,
		ContractID:        metadata.ContractID,
		Algorithm:         metadata.Algorithm,
		KDF:               metadata.KDF,
		Identity:          metadata.Identity,
		RootKeyGeneration: metadata.RootKeyGeneration,
	})
	return raw
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func validRoot(root *RootKey) bool {
	return root != nil && root.generation > 0 && !root.closed && !allZero(root.key[:])
}

func validateMetadataShape(metadata Metadata) error {
	if metadata.Version != metadataVersion ||
		metadata.ContractID != contractID() ||
		metadata.Algorithm != algorithmName ||
		metadata.KDF != kdfName ||
		metadata.Identity.validate() != nil ||
		metadata.RootKeyGeneration == 0 ||
		len(metadata.WrapNonce) != 12 ||
		len(metadata.WrappedDEK) != 48 {
		return ErrProfileCorrupt
	}
	return nil
}
