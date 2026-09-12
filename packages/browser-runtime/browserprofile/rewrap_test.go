package browserprofile

import (
	"fmt"
	"path/filepath"
)

// Test-only key rotation fixture. No runtime rotation API is exposed.
func (store *Store) rewrap(
	identity Identity,
	roots map[uint64]*RootKey,
	newRoot *RootKey,
) error {
	if store == nil || identity.validate() != nil || !validRoot(newRoot) {
		return ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lockFile == nil {
		return ErrProfileStoreClosed
	}

	snapshot, err := store.openSnapshot(identity, roots)
	if err != nil {
		return store.handleOpenError(identity, err)
	}
	defer snapshot.close()
	if err := authenticatePayload(snapshot.payload, snapshot.payloadCipher); err != nil {
		snapshot.close()
		return store.handleOpenError(identity, err)
	}
	oldRoot := roots[snapshot.metadata.RootKeyGeneration]
	rewrapped, err := store.protector.rewrap(snapshot.metadata, identity, oldRoot, newRoot)
	if err != nil {
		return store.handleOpenError(identity, err)
	}
	raw, err := marshalMetadata(rewrapped)
	if err != nil {
		return err
	}
	if _, err := atomicWriteFile(filepath.Join(snapshot.dir, metadataFileName), raw, 0o600); err != nil {
		return fmt.Errorf("commit rewrapped browser profile metadata: %w", err)
	}
	return nil
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
