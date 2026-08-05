package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	// DocRoleEncodingKey is the shared docs-backend schema marker key.
	DocRoleEncodingKey = "role_encoding"
	// DocRoleEncodingAppendV1 identifies reader=1, writer=2, admin=3,
	// commenter=4. Existing rows are not recoded.
	DocRoleEncodingAppendV1 = "append-v1"
)

// ErrDocRoleEncodingUnverified means the shared role integers cannot safely be
// interpreted. Callers must fail closed rather than risk privilege escalation.
var ErrDocRoleEncodingUnverified = errors.New("doc_member role encoding unverified")

// RequireAppendRoleEncoding verifies the shared runtime contract. Missing,
// unknown, and former ordered-v2 markers are rejected; this function never
// mutates schema or role data.
func RequireAppendRoleEncoding(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("%w: nil database", ErrDocRoleEncodingUnverified)
	}
	var value string
	err := db.QueryRowContext(ctx,
		"SELECT meta_value FROM docs_metadata WHERE meta_key=?",
		DocRoleEncodingKey).Scan(&value)
	if err != nil {
		return fmt.Errorf("%w: read marker: %v", ErrDocRoleEncodingUnverified, err)
	}
	if value != DocRoleEncodingAppendV1 {
		return fmt.Errorf("%w: want %q, found %q", ErrDocRoleEncodingUnverified, DocRoleEncodingAppendV1, value)
	}
	return nil
}
