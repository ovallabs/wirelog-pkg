//go:build integration

package wirelog

// Requires the docker-compose Postgres: docker compose up -d, then
// go test -tags integration -run TestWriterAgainstRealPostgres .

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const integrationDBURL = "postgres://wirelog:wirelog@localhost:5439/wirelog?sslmode=disable"

// TestWriterAgainstRealPostgres exercises New → enqueue → Close against the
// docker-compose database, verifying row counts, B15 NULL mapping, and jsonb
// containment.
func TestWriterAgainstRealPostgres(t *testing.T) {
	ctx := context.Background()
	provider := fmt.Sprintf("it-%d", time.Now().UnixNano()) // unique per run

	wl, err := New(ctx, integrationDBURL, WithAutoMigrate(true), WithBatchSize(2), WithFlushInterval(100*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v (is docker compose up?)", err)
	}

	full := record{
		provider: provider, consumer: "it", operation: "payout.execute",
		endpoint: "/v1/transfers/{id}", path: "/v1/transfers/123", method: "POST",
		statusCode: 200, outcome: outcomeSuccess, latencyMS: 42, requestSize: 10, responseSize: 20,
		internalRef: "ref-1", idempotencyKey: "idem-1",
		requestHeaders:  map[string][]string{"Content-Type": {"application/json"}},
		requestBody:     []byte(`{"a":1}`),
		responseHeaders: map[string][]string{"Content-Type": {"application/json"}},
		responseBody:    []byte(`{"b":2}`),
		tags:            map[string]any{"k": "v"},
	}
	empty := record{provider: provider, outcome: outcomeNetwork, callErr: "dial tcp: refused"}
	third := record{provider: provider, outcome: outcomeTimeout, callErr: "context deadline exceeded"}
	for _, rec := range []record{full, empty, third} {
		wl.ch <- rec
	}
	wl.Close() // drain, final flush, pool close (B13)

	pool, err := pgxpool.New(ctx, integrationDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// deferred after pool.Close so it runs first (LIFO): the pool is still open
	defer func() {
		_, _ = pool.Exec(context.Background(), "delete from provider_api_logs where provider = $1", provider)
	}()

	var n int
	if err := pool.QueryRow(ctx, "select count(*) from provider_api_logs where provider = $1", provider).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("rows = %d, want 3 (Close must flush the remainder)", n)
	}

	// NULL mapping (B15) on the sparse row.
	var statusNull, refNull, headersNull, bodyNull, tagsNull bool
	var consumer string
	err = pool.QueryRow(ctx,
		`select status_code is null, internal_ref is null, request_headers is null,
		        request_body is null, tags is null, consumer
		 from provider_api_logs where provider = $1 and outcome = $2`,
		provider, outcomeNetwork).Scan(&statusNull, &refNull, &headersNull, &bodyNull, &tagsNull, &consumer)
	if err != nil {
		t.Fatal(err)
	}
	if !statusNull || !refNull || !headersNull || !bodyNull || !tagsNull {
		t.Errorf("zero/empty/nil fields must be SQL NULL: status=%v ref=%v headers=%v body=%v tags=%v",
			statusNull, refNull, headersNull, bodyNull, tagsNull)
	}
	if consumer != "" {
		t.Errorf("consumer = %q, want '' (non-nullable text keeps its default)", consumer)
	}

	// jsonb round-trip and containment on the full row.
	var body string
	err = pool.QueryRow(ctx,
		`select request_body::text from provider_api_logs
		 where provider = $1 and request_body @> '{"a":1}'::jsonb`, provider).Scan(&body)
	if err != nil {
		t.Fatalf("jsonb containment query failed (was the body stored as jsonb?): %v", err)
	}

	var errText pgx.Row = pool.QueryRow(ctx,
		"select error from provider_api_logs where provider = $1 and outcome = $2", provider, outcomeTimeout)
	var e string
	if err := errText.Scan(&e); err != nil || e != "context deadline exceeded" {
		t.Errorf("error column = %q (%v), want stored error text", e, err)
	}
}
