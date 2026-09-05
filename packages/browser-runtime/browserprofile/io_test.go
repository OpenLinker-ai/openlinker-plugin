package browserprofile

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type oneByteWriter struct {
	buffer bytes.Buffer
}

func (writer *oneByteWriter) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return writer.buffer.Write(value[:1])
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) {
	return 0, nil
}

func TestPayloadHandlesPartialWritesAndRejectsZeroProgress(t *testing.T) {
	t.Parallel()
	protector := newProtector(nil)
	root := testRootKey(t, 1, 0x11)
	defer root.Close()
	metadata, encryptor, err := protector.create(testIdentity(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer encryptor.close()

	encrypted := &oneByteWriter{}
	if err := encryptor.encrypt(encrypted, bytes.NewReader([]byte("profile"))); err != nil {
		t.Fatal(err)
	}
	decryptor, err := protector.open(metadata, testIdentity(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer decryptor.close()
	decrypted := &oneByteWriter{}
	if err := decryptor.decrypt(decrypted, bytes.NewReader(encrypted.buffer.Bytes())); err != nil {
		t.Fatal(err)
	}
	if got := decrypted.buffer.String(); got != "profile" {
		t.Fatalf("decrypted payload = %q, want profile", got)
	}
	if err := encryptor.encrypt(zeroWriter{}, bytes.NewReader(nil)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress writer error = %v, want %v", err, io.ErrShortWrite)
	}
}
