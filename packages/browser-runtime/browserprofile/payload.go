package browserprofile

import (
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	payloadChunkSize = 1 << 20
	payloadMagic     = "OLBRPF01"
	noncePrefixSize  = 8
)

func (cipher *payloadCipher) encrypt(writer io.Writer, reader io.Reader) error {
	if err := cipher.validate(); err != nil {
		return err
	}
	if writer == nil || reader == nil {
		return ErrInvalidConfiguration
	}
	gcm, err := newGCM(cipher.key[:])
	if err != nil || gcm.NonceSize() != noncePrefixSize+4 {
		return ErrInvalidConfiguration
	}
	prefix := make([]byte, noncePrefixSize)
	if _, err := io.ReadFull(cipher.random, prefix); err != nil {
		return fmt.Errorf("generate browser profile payload nonce: %w", err)
	}
	if err := writeAll(writer, []byte(payloadMagic)); err != nil {
		return fmt.Errorf("write browser profile payload header: %w", err)
	}
	if err := writeAll(writer, prefix); err != nil {
		return fmt.Errorf("write browser profile payload nonce: %w", err)
	}

	buffer := make([]byte, payloadChunkSize)
	var index uint32
	for {
		read, readErr := io.ReadFull(reader, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			clear(buffer)
			return fmt.Errorf("read browser profile payload: %w", readErr)
		}
		if read > 0 {
			if index == math.MaxUint32 {
				clear(buffer)
				return errors.New("browser profile payload has too many chunks")
			}
			if err := writePayloadRecord(writer, gcm, prefix, index, buffer[:read], false, cipher.identity); err != nil {
				clear(buffer)
				return err
			}
			index++
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	clear(buffer)
	return writePayloadRecord(writer, gcm, prefix, index, nil, true, cipher.identity)
}

func (cipher *payloadCipher) decrypt(writer io.Writer, reader io.Reader) error {
	if err := cipher.validate(); err != nil {
		return err
	}
	if writer == nil || reader == nil {
		return ErrInvalidConfiguration
	}
	gcm, err := newGCM(cipher.key[:])
	if err != nil || gcm.NonceSize() != noncePrefixSize+4 {
		return ErrInvalidConfiguration
	}
	header := make([]byte, len(payloadMagic)+noncePrefixSize)
	if _, err := io.ReadFull(reader, header); err != nil || string(header[:len(payloadMagic)]) != payloadMagic {
		return ErrProfileCorrupt
	}
	prefix := header[len(payloadMagic):]
	var index uint32
	for {
		var lengthBytes [4]byte
		if _, err := io.ReadFull(reader, lengthBytes[:]); err != nil {
			return ErrProfileCorrupt
		}
		length := binary.BigEndian.Uint32(lengthBytes[:])
		if length > payloadChunkSize {
			return ErrProfileCorrupt
		}
		sealed := make([]byte, int(length)+gcm.Overhead())
		if _, err := io.ReadFull(reader, sealed); err != nil {
			return ErrProfileCorrupt
		}
		final := length == 0
		plaintext, err := gcm.Open(
			nil,
			payloadNonce(prefix, index),
			sealed,
			payloadAAD(cipher.identity, index, length, final),
		)
		clear(sealed)
		if err != nil || len(plaintext) != int(length) {
			clear(plaintext)
			return ErrProfileCorrupt
		}
		if final {
			clear(plaintext)
			var trailing [1]byte
			read, trailingErr := io.ReadFull(reader, trailing[:])
			if read != 0 || !errors.Is(trailingErr, io.EOF) {
				return ErrProfileCorrupt
			}
			return nil
		}
		if err := writeAll(writer, plaintext); err != nil {
			clear(plaintext)
			return fmt.Errorf("write decrypted browser profile payload: %w", err)
		}
		clear(plaintext)
		if index == math.MaxUint32 {
			return ErrProfileCorrupt
		}
		index++
	}
}

func (cipher *payloadCipher) validate() error {
	if cipher == nil || cipher.closed {
		return ErrKeyClosed
	}
	if cipher.identity.validate() != nil || allZero(cipher.key[:]) || cipher.random == nil {
		return ErrInvalidConfiguration
	}
	return nil
}

func writePayloadRecord(
	writer io.Writer,
	gcm cipher.AEAD,
	prefix []byte,
	index uint32,
	plaintext []byte,
	final bool,
	identity Identity,
) error {
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], uint32(len(plaintext)))
	if err := writeAll(writer, lengthBytes[:]); err != nil {
		return fmt.Errorf("write browser profile chunk length: %w", err)
	}
	sealed := gcm.Seal(
		nil,
		payloadNonce(prefix, index),
		plaintext,
		payloadAAD(identity, index, uint32(len(plaintext)), final),
	)
	err := writeAll(writer, sealed)
	clear(sealed)
	if err != nil {
		return fmt.Errorf("write browser profile chunk: %w", err)
	}
	return nil
}

func writeAll(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if written < 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func payloadNonce(prefix []byte, index uint32) []byte {
	nonce := make([]byte, noncePrefixSize+4)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[noncePrefixSize:], index)
	return nonce
}

func payloadAAD(identity Identity, index uint32, length uint32, final bool) []byte {
	raw, _ := json.Marshal(struct {
		ContractID string   `json:"contract_id"`
		Purpose    string   `json:"purpose"`
		Identity   Identity `json:"identity"`
		ChunkIndex uint32   `json:"chunk_index"`
		Length     uint32   `json:"length"`
		Final      bool     `json:"final"`
	}{
		ContractID: contractID(),
		Purpose:    "profile-payload",
		Identity:   identity,
		ChunkIndex: index,
		Length:     length,
		Final:      final,
	})
	return raw
}
