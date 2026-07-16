// migrate.go — the archive's floor plan: the embedded DDL for
// provider_api_logs, applied only when auto-migrate is enabled.

package wirelog

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// execer is the slice of pgxpool.Pool that migration needs; kept small so
// tests can fake it without a database.
type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// schemaDDL is the FRD schema, embedded verbatim.
const schemaDDL = `create table if not exists provider_api_logs (
    id               bigint generated always as identity primary key,
    created_at       timestamptz not null default now(),
    provider         text        not null,
    consumer         text        not null default '',
    operation        text        not null default '',
    endpoint         text        not null default '',
    path             text        not null default '',
    method           text        not null default '',
    status_code      int,
    outcome          text        not null,
    latency_ms       bigint      not null default 0,
    request_size     bigint      not null default 0,
    response_size    bigint      not null default 0,
    internal_ref     text,
    idempotency_key  text,
    request_headers  jsonb,
    request_body     jsonb,
    response_headers jsonb,
    response_body    jsonb,
    error            text,
    tags             jsonb
);
create index if not exists idx_pal_provider_time on provider_api_logs (provider, created_at desc);
create index if not exists idx_pal_consumer_time on provider_api_logs (consumer, created_at desc);
create index if not exists idx_pal_internal_ref  on provider_api_logs (internal_ref) where internal_ref is not null;
create index if not exists idx_pal_idem_key      on provider_api_logs (idempotency_key) where idempotency_key is not null;
create index if not exists idx_pal_failures      on provider_api_logs (created_at desc) where outcome <> 'success';
create index if not exists idx_pal_req_body_gin  on provider_api_logs using gin (request_body  jsonb_path_ops);
create index if not exists idx_pal_resp_body_gin on provider_api_logs using gin (response_body jsonb_path_ops);
`

// migrate applies the embedded DDL; idempotent via IF NOT EXISTS throughout.
func migrate(ctx context.Context, db execer) error {
	_, err := db.Exec(ctx, schemaDDL)
	return err
}
