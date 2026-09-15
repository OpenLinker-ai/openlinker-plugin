package browserprofile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const (
	MigrationRequestSchema       = "openlinker.browser.profile-migration.v1"
	migrationReportSchema        = "openlinker.browser.profile-migration-report.v1"
	migrationPendingName         = ".migration-pending.json"
	migrationReceiptName         = "migration-receipt.json"
	migrationMaxRequest          = 16 << 10
	migrationMaxCiphertext int64 = 512 << 20
	migrationMaxPlaintext  int64 = 448 << 20
	migrationRetention           = 30 * 24 * time.Hour
)

type MigrationMode string

const (
	MigrationExecute  MigrationMode = "execute"
	MigrationCheck    MigrationMode = "check"
	MigrationFinalize MigrationMode = "finalize"
)

// MigrationRequest is an explicit offline operation, not a Core authorization
// ticket. ExecuteProfileMigration accepts only its private on-disk encoding.
type MigrationRequest struct {
	Schema            string             `json:"schema"`
	OperationID       string             `json:"operation_id"`
	SourceContract    string             `json:"source_contract"`
	TargetContract    string             `json:"target_contract"`
	SourceStore       string             `json:"source_store"`
	DestinationStore  string             `json:"destination_store"`
	RootKeyFile       string             `json:"root_key_file"`
	ExpectedIdentity  Identity           `json:"expected_identity"`
	RootKeyGeneration uint64             `json:"root_key_generation"`
	Source            MigrationSnapshot  `json:"source"`
	Retention         MigrationRetention `json:"retention"`
}

type MigrationSnapshot struct {
	ProfileDirectorySHA256 string `json:"profile_directory_sha256"`
	MetadataSHA256         string `json:"metadata_sha256"`
	CheckpointIDSHA256     string `json:"checkpoint_id_sha256"`
	PayloadSHA256          string `json:"payload_sha256"`
	CurrentPointerMTime    string `json:"current_pointer_mtime"`
}

type MigrationRetention struct {
	Mode                          string `json:"mode"`
	Anchor                        string `json:"anchor,omitempty"`
	AuthorizationSHA256           string `json:"authorization_sha256,omitempty"`
	PriorRestorationReceiptSHA256 string `json:"prior_restoration_receipt_sha256,omitempty"`
}

// MigrationReport contains no raw identity, paths, keys, plaintext or plaintext
// hashes. A published reservation is NOT necessarily safe to activate.
type MigrationReport struct {
	Schema              string             `json:"schema"`
	Mode                MigrationMode      `json:"mode"`
	OperationSHA256     string             `json:"operation_sha256,omitempty"`
	RequestSHA256       string             `json:"request_sha256,omitempty"`
	IdentitySHA256      string             `json:"identity_sha256,omitempty"`
	SourceContract      string             `json:"source_contract,omitempty"`
	TargetContract      string             `json:"target_contract,omitempty"`
	ProfileGeneration   uint64             `json:"profile_generation,omitempty"`
	RootKeyGeneration   uint64             `json:"root_key_generation,omitempty"`
	Source              MigrationSnapshot  `json:"source"`
	Target              MigrationSnapshot  `json:"target"`
	Retention           MigrationRetention `json:"retention"`
	SourceAuthenticated bool               `json:"source_authenticated"`
	TargetAuthenticated bool               `json:"target_authenticated"`
	PlaintextEqual      bool               `json:"plaintext_equal"`
	SourceUnchanged     bool               `json:"source_unchanged"`
	Published           bool               `json:"published"`
	ActivationReady     bool               `json:"activation_ready"`
	AlreadyFinalized    bool               `json:"already_finalized"`
	Stage               string             `json:"stage"`
	Status              string             `json:"status"`
	FailureCode         string             `json:"failure_code,omitempty"`
	FailureType         string             `json:"failure_type,omitempty"`
	ElapsedMillis       int64              `json:"elapsed_ms"`
}

var ErrProfileMigrationFailed = errors.New("browser profile migration failed; inspect the redacted report")

// ExecuteProfileMigration is the sole offline migration entry point. Errors
// are intentionally fixed; consumers must emit the report, not upstream I/O.
func ExecuteProfileMigration(ctx context.Context, requestPath string, mode MigrationMode) (report MigrationReport, err error) {
	start := time.Now()
	report = MigrationReport{Schema: migrationReportSchema, Mode: mode, Stage: "request", Status: "failed"}
	defer func() {
		report.ElapsedMillis = time.Since(start).Milliseconds()
		if err != nil {
			if report.FailureCode == "" {
				report.FailureCode = "migration_failed"
			}
			if report.FailureType == "" {
				report.FailureType = "validation_or_io"
			}
			err = ErrProfileMigrationFailed
		}
	}()
	if ctx == nil || (mode != MigrationExecute && mode != MigrationCheck && mode != MigrationFinalize) {
		report.FailureCode = "invalid_request"
		return report, ErrProfileMigrationFailed
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	err = executeProfileMigration(ctx, requestPath, mode, &report)
	if err != nil && ctx.Err() != nil {
		report.FailureCode, report.FailureType = "cancelled", "cancellation"
	}
	if err == nil {
		report.Status = "success"
	}
	return report, err
}

func validateMigrationRequest(request MigrationRequest, now time.Time) (time.Time, error) {
	if request.Schema != MigrationRequestSchema || !validUUID(request.OperationID) ||
		request.SourceContract != string(storageContractV1) || request.TargetContract != string(storageContractV2) ||
		request.ExpectedIdentity.validate() != nil || request.RootKeyGeneration == 0 {
		return time.Time{}, ErrInvalidConfiguration
	}
	for _, path := range []string{request.SourceStore, request.DestinationStore, request.RootKeyFile} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return time.Time{}, ErrInvalidConfiguration
		}
	}
	if pathsOverlap(request.SourceStore, request.DestinationStore) || pathsOverlap(request.DestinationStore, request.RootKeyFile) {
		return time.Time{}, ErrInvalidConfiguration
	}
	if request.Source.ProfileDirectorySHA256 != profileDigestForContract(request.ExpectedIdentity, storageContractV1) {
		return time.Time{}, ErrIdentityMismatch
	}
	for _, hash := range []string{request.Source.ProfileDirectorySHA256, request.Source.MetadataSHA256, request.Source.CheckpointIDSHA256, request.Source.PayloadSHA256} {
		if !validProfileDigest(hash) {
			return time.Time{}, ErrInvalidConfiguration
		}
	}
	oldTime, err := time.Parse(time.RFC3339Nano, request.Source.CurrentPointerMTime)
	if err != nil || oldTime.After(now) {
		return time.Time{}, ErrInvalidConfiguration
	}
	anchor := oldTime
	switch request.Retention.Mode {
	case "preserve":
		if request.Retention.Anchor != "" || request.Retention.AuthorizationSHA256 != "" || request.Retention.PriorRestorationReceiptSHA256 != "" {
			return time.Time{}, ErrInvalidConfiguration
		}
	case "restore-approved-anchor":
		anchor, err = time.Parse(time.RFC3339Nano, request.Retention.Anchor)
		if err != nil || !validProfileDigest(request.Retention.AuthorizationSHA256) || !validProfileDigest(request.Retention.PriorRestorationReceiptSHA256) || anchor.Before(oldTime) {
			return time.Time{}, ErrInvalidConfiguration
		}
	default:
		return time.Time{}, ErrInvalidConfiguration
	}
	if anchor.After(now) || anchor.Before(now.Add(-migrationRetention)) {
		return time.Time{}, ErrInvalidConfiguration
	}
	return anchor, nil
}

func pathsOverlap(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}

func migrationSHA(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

type migrationContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *migrationContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

// Walk the JSON token tree first because DisallowUnknownFields alone accepts
// duplicate members, including duplicates inside expected_identity.
func strictMigrationJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 16 {
			return ErrInvalidConfiguration
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrInvalidConfiguration
				}
				seen[name] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return ErrInvalidConfiguration
		}
		_, err = decoder.Token()
		return err
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidConfiguration
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidConfiguration
	}
	return nil
}
