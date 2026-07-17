package wirelog

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// recordingExecer captures executed SQL so migration is testable without a
// database (Q8 pattern).
type recordingExecer struct {
	sqls []string
	err  error
}

// Exec records the statement and returns the configured error.
func (r *recordingExecer) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sqls = append(r.sqls, sql)
	return pgconn.CommandTag{}, r.err
}

// TestMigrateExecutesEmbeddedDDLOnce checks migrate runs the embedded DDL in
// a single Exec call.
func TestMigrateExecutesEmbeddedDDLOnce(t *testing.T) {
	db := &recordingExecer{}
	if err := migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate error: %v", err)
	}
	if len(db.sqls) != 1 || db.sqls[0] != schemaDDL {
		t.Fatalf("migrate must execute the embedded DDL exactly once, got %d execs", len(db.sqls))
	}
}

// TestMigratePropagatesError checks a failed DDL exec surfaces to the caller
// so New can abort.
func TestMigratePropagatesError(t *testing.T) {
	sentinel := errors.New("permission denied")
	db := &recordingExecer{err: sentinel}
	if err := migrate(context.Background(), db); !errors.Is(err, sentinel) {
		t.Fatalf("migrate error = %v, want sentinel", err)
	}
}

// TestSchemaDDLMatchesFRD asserts every column and index of the FRD schema
// appears verbatim in the embedded DDL.
func TestSchemaDDLMatchesFRD(t *testing.T) {
	required := []string{
		"create table if not exists provider_api_logs",
		"id               bigint generated always as identity primary key",
		"created_at       timestamptz not null default now()",
		"provider         text        not null",
		"consumer         text        not null default ''",
		"operation        text        not null default ''",
		"endpoint         text        not null default ''",
		"path             text        not null default ''",
		"method           text        not null default ''",
		"status_code      int",
		"outcome          text        not null",
		"latency_ms       bigint      not null default 0",
		"request_size     bigint      not null default 0",
		"response_size    bigint      not null default 0",
		"internal_ref     text",
		"idempotency_key  text",
		"request_headers  jsonb",
		"request_body     jsonb",
		"response_headers jsonb",
		"response_body    jsonb",
		"error            text",
		"tags             jsonb",
		"idx_pal_provider_time on provider_api_logs (provider, created_at desc)",
		"idx_pal_consumer_time on provider_api_logs (consumer, created_at desc)",
		"idx_pal_internal_ref  on provider_api_logs (internal_ref) where internal_ref is not null",
		"idx_pal_idem_key      on provider_api_logs (idempotency_key) where idempotency_key is not null",
		"idx_pal_failures      on provider_api_logs (created_at desc) where outcome <> 'success'",
		"idx_pal_req_body_gin  on provider_api_logs using gin (request_body  jsonb_path_ops)",
		"idx_pal_resp_body_gin on provider_api_logs using gin (response_body jsonb_path_ops)",
	}
	for _, want := range required {
		if !strings.Contains(schemaDDL, want) {
			t.Errorf("schemaDDL missing verbatim fragment: %q", want)
		}
	}
}
