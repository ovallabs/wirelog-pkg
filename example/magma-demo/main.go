// magma-demo drives simulated Magma payout traffic through wirelog against a
// stub server, then verifies the rows captured in Postgres. It exits non-zero
// on any failed assertion, so it doubles as an end-to-end test.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ovallabs/wirelog"
)

const (
	defaultDBURL = "postgres://wirelog:wirelog@localhost:5439/wirelog?sslmode=disable"
	msisdn       = "+237670000001"
)

// databaseURL resolves the DSN from the conventional env var, falling back to the docker-compose default.
func databaseURL() string {
	if url := os.Getenv(wirelog.EnvDatabaseURL); url != "" {
		return url
	}
	return defaultDBURL
}

// main reports the run's verdict and sets the exit code.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "magma-demo: FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("magma-demo: all assertions passed")
}

// run wires wirelog to the stub server, drives the traffic, and verifies the
// captured rows.
func run() error {
	ctx := context.Background()

	srv := newStubMagma()
	defer srv.Close()

	dsn := databaseURL()
	wl, err := wirelog.New(ctx, dsn,
		wirelog.WithDefaultConsumer("magma-demo"),
		wirelog.WithAutoMigrate(true),
		wirelog.WithLogger(log.New(os.Stdout, "wirelog: ", log.LstdFlags)))
	if err != nil {
		return fmt.Errorf("connect to Postgres (is `docker compose up -d` running?): %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	// reset rows from previous runs so the report counts are deterministic
	if _, err := pool.Exec(ctx, "delete from provider_api_logs where provider = 'magma'"); err != nil {
		return err
	}

	cfg := wirelog.NewConfig("magma",
		wirelog.WithCaptureBodies(true),
		wirelog.WithExtraMaskFields("sender_first_name", "sender_last_name", "sender_address"))

	// providers often own their transport (egress proxy, custom TLS); WrapTransport
	// layers capture on top of it instead of replacing it — the same path httpx uses
	providerTransport := otelhttp.NewTransport(&http.Transport{MaxIdleConns: 100})
	client := &http.Client{Transport: wl.WrapTransport(cfg, providerTransport)}

	driveTraffic(ctx, client, srv.URL)

	wl.Close()
	fmt.Printf("dropped records: %d\n\n", wl.Dropped())

	return report(ctx, pool)
}

// newStubMagma starts the in-process stub provider.
func newStubMagma() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /partner/balance", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		writeJSON(w, 200, map[string]any{"currency": "XAF", "available": 1_500_000})
	})
	mux.HandleFunc("POST /v1/transfers", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Demo-Fail") == "provider" {
			writeJSON(w, 422, map[string]any{"error": "insufficient_balance", "message": "wallet balance too low"})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, 200, map[string]any{
			"status":           "accepted",
			"transfer_token":   "tk_5f0c2f8e2f5a",
			"receiver_account": req["receiver_account"], // echo the MSISDN back
			"amount":           req["amount"],
		})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"access_token": "secret-token-abc", "expires_in": 3600})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		writeJSON(w, 200, map[string]any{"status": "finally"})
	})
	return httptest.NewServer(mux)
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// driveTraffic exercises every outcome; individual call errors are expected
// (timeout, network) and never abort the demo.
func driveTraffic(ctx context.Context, client *http.Client, baseURL string) {
	// 1. balance check → success
	balCtx := wirelog.WithOperation(ctx, "balance.check")
	do(client, mustRequest(balCtx, http.MethodGet, baseURL+"/partner/balance", ""))

	// 2. successful transfer with full annotations → success
	payCtx := wirelog.WithRef(ctx, "PYT-2026-0001")
	payCtx = wirelog.WithOperation(payCtx, "payout.execute")
	payCtx = wirelog.WithIdempotencyKey(payCtx, "idem-PYT-2026-0001")
	payCtx = wirelog.WithTags(payCtx, map[string]any{"batch": "demo-run"})
	payload := fmt.Sprintf(`{"receiver_account":%q,"amount":150000,"currency":"XAF",`+
		`"sender_first_name":"Ova","sender_last_name":"Payments","sender_address":"Douala"}`, msisdn)
	do(client, mustRequest(payCtx, http.MethodPost, baseURL+"/v1/transfers", payload))

	// 3. provider rejection → provider_error, its own ref so row counts stay 1:1
	failCtx := wirelog.WithRef(ctx, "PYT-2026-0002")
	failCtx = wirelog.WithOperation(failCtx, "payout.execute")
	failCtx = wirelog.WithIdempotencyKey(failCtx, "idem-PYT-2026-0002")
	failReq := mustRequest(failCtx, http.MethodPost, baseURL+"/v1/transfers", payload)
	failReq.Header.Set("X-Demo-Fail", "provider")
	do(client, failReq)

	// 4. 500ms request deadline against /slow → timeout. A context deadline,
	// not http.Client.Timeout: the latter cancels via a timer that can reach
	// the transport as a bare "request canceled" instead of DeadlineExceeded.
	slowCtx, cancel := context.WithTimeout(wirelog.WithOperation(ctx, "slow.call"), 500*time.Millisecond)
	defer cancel()
	do(client, mustRequest(slowCtx, http.MethodGet, baseURL+"/slow", ""))

	// 5. closed port → network
	do(client, mustRequest(ctx, http.MethodGet, "http://"+closedAddr(), ""))

	// 6. health probe → excluded, zero rows
	do(client, mustRequest(ctx, http.MethodGet, baseURL+"/health", ""))

	// 7. token call → skip-body path, row with NULL bodies
	do(client, mustRequest(ctx, http.MethodPost, baseURL+"/oauth/token",
		`{"client_id":"magma-demo","client_secret":"hunter2"}`))
}

// mustRequest builds a request with demo auth headers, panicking on bad input.
func mustRequest(ctx context.Context, method, url, body string) *http.Request {
	var r *http.Request
	var err error
	if body == "" {
		r, err = http.NewRequestWithContext(ctx, method, url, nil)
	} else {
		r, err = http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err == nil {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		panic(err)
	}
	r.Header.Set("Authorization", "Bearer live-magma-key")
	return r
}

// do executes one call and prints its status or error; failures are expected
// for the timeout and network scenarios.
func do(client *http.Client, req *http.Request) {
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("%-6s %-40s -> %v\n", req.Method, req.URL.Path, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := json.Marshal(resp.Status)
	fmt.Printf("%-6s %-40s -> %s\n", req.Method, req.URL.Path, body)
}

// closedAddr returns an address that refuses connections: bind a port, then
// release it before dialing.
func closedAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// report queries Postgres and asserts what the run must have produced.
func report(ctx context.Context, pool *pgxpool.Pool) error {
	var failures []string
	fail := func(format string, args ...any) { failures = append(failures, fmt.Sprintf(format, args...)) }

	fmt.Println("rows per outcome:")
	counts := map[string]int{}
	rows, err := pool.Query(ctx,
		"select outcome, count(*) from provider_api_logs where provider = 'magma' group by outcome order by outcome")
	if err != nil {
		return err
	}
	for rows.Next() {
		var outcome string
		var n int
		if err := rows.Scan(&outcome, &n); err != nil {
			return err
		}
		counts[outcome] = n
		fmt.Printf("  %-15s %d\n", outcome, n)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for outcome, want := range map[string]int{"success": 3, "provider_error": 1, "timeout": 1, "network": 1} {
		if counts[outcome] != want {
			fail("outcome %s: got %d rows, want %d", outcome, counts[outcome], want)
		}
	}

	var healthRows int
	if err := pool.QueryRow(ctx,
		"select count(*) from provider_api_logs where provider = 'magma' and path = '/health'").Scan(&healthRows); err != nil {
		return err
	}
	if healthRows != 0 {
		fail("/health produced %d rows, want 0 (ExcludePaths)", healthRows)
	}

	var tokenRows, tokenNullBodies int
	if err := pool.QueryRow(ctx,
		`select count(*), count(*) filter (where request_body is null and response_body is null)
		 from provider_api_logs where provider = 'magma' and path = '/oauth/token'`).Scan(&tokenRows, &tokenNullBodies); err != nil {
		return err
	}
	if tokenRows != 1 || tokenNullBodies != 1 {
		fail("/oauth/token: got %d rows with %d NULL-body rows, want 1/1 (SkipBodyPaths)", tokenRows, tokenNullBodies)
	}

	var refRows int
	if err := pool.QueryRow(ctx,
		"select count(*) from provider_api_logs where provider = 'magma' and internal_ref = 'PYT-2026-0001' and idempotency_key = 'idem-PYT-2026-0001'").Scan(&refRows); err != nil {
		return err
	}
	if refRows != 1 {
		fail("annotated transfer: got %d rows by ref+idempotency key, want 1", refRows)
	}

	var msisdnHits int
	if err := pool.QueryRow(ctx,
		`select count(*) from provider_api_logs where provider = 'magma'
		 and (request_body::text like $1 or response_body::text like $1)`,
		"%"+msisdn+"%").Scan(&msisdnHits); err != nil {
		return err
	}
	fmt.Printf("stored bodies containing %s: %d\n", msisdn, msisdnHits)
	if msisdnHits != 0 {
		fail("MSISDN %s appears in %d stored bodies, want 0 (masking)", msisdn, msisdnHits)
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d assertion(s) failed:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	return nil
}
