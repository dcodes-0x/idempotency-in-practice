package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

var ErrSimulatedResponseLoss = errors.New(
	"simulated network failure: HTTP response lost after commit",
)

// DropResponseTransport allows a real HTTP request to complete,
// then discards the response and returns a network-like error.
//
// This simulates a client that cannot observe a successful
// database commit because the response was lost.
type DropResponseTransport struct {
	Base http.RoundTripper
}

func (t *DropResponseTransport) RoundTrip(
	req *http.Request,
) (*http.Response, error) {

	resp, err := t.Base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// The server has already produced its HTTP response.
	// Our handler only writes the success response after
	// the PostgreSQL transaction commits.
	_, discardErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()

	if discardErr != nil {
		return nil, discardErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	// Hide the successful response from the client.
	return nil, ErrSimulatedResponseLoss
}

func TestLostResponseAfterCommit(t *testing.T) {
	// ------------------------------------------------
	// 1. Initialize test environment
	// ------------------------------------------------

	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create database pool: %v", err)
	}

	// Registered first so the pool closes last.
	t.Cleanup(func() {
		db.Close()
	})

	if err := db.Ping(ctx); err != nil {
		t.Fatalf("database ping failed: %v", err)
	}

	// ------------------------------------------------
	// 2. Construct application dependencies
	// ------------------------------------------------

	repo := NewPostgresPaymentRepository(db)
	service := NewPaymentService(repo)
	handler := NewPaymentHandler(service)

	server := httptest.NewServer(
		http.HandlerFunc(handler.CreatePayment),
	)

	t.Cleanup(func() {
		server.Close()
	})

	// ------------------------------------------------
	// 3. Generate isolated test identifiers
	// ------------------------------------------------

	orderID := uuid.New()
	idempotencyKey := "lost-response-" + uuid.NewString()

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		// Data cleanup runs before pool closure.
		if _, err := db.Exec(
			cleanupCtx,
			`DELETE FROM idempotency_keys
			 WHERE idempotency_key = $1`,
			idempotencyKey,
		); err != nil {
			t.Errorf("cleanup idempotency key: %v", err)
		}

		if _, err := db.Exec(
			cleanupCtx,
			`DELETE FROM payments
			 WHERE order_id = $1`,
			orderID,
		); err != nil {
			t.Errorf("cleanup payments: %v", err)
		}
	})

	payload, err := json.Marshal(CreatePaymentRequest{
		OrderID: orderID.String(),
		Amount:  1000,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	// Helper to create identical logical requests.
	newPaymentRequest := func() *http.Request {
		t.Helper()

		req, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/payments",
			bytes.NewReader(payload),
		)
		if err != nil {
			t.Fatalf("create HTTP request: %v", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)

		return req
	}

	// ------------------------------------------------
	// 4. Send first request and lose its response
	// ------------------------------------------------

	t.Log("Step 1: Sending first payment request")

	lossClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &DropResponseTransport{
			Base: http.DefaultTransport,
		},
	}

	firstResp, firstErr := lossClient.Do(newPaymentRequest())
	if firstResp != nil {
		firstResp.Body.Close()
		t.Fatal("expected no visible HTTP response")
	}

	if !errors.Is(firstErr, ErrSimulatedResponseLoss) {
		t.Fatalf(
			"expected simulated response loss, got: %v",
			firstErr,
		)
	}

	t.Log("Step 2: Client experienced a simulated network failure")

	// ------------------------------------------------
	// 5. Verify the first transaction committed
	// ------------------------------------------------

	var firstPaymentID uuid.UUID

	err = db.QueryRow(
		ctx,
		`SELECT id
		 FROM payments
		 WHERE order_id = $1`,
		orderID,
	).Scan(&firstPaymentID)
	if err != nil {
		t.Fatalf("first payment was not committed: %v", err)
	}

	var storedStatus string
	var storedHTTPStatus int
	var storedBody string

	err = db.QueryRow(
		ctx,
		`SELECT status, response_status, response_body
		 FROM idempotency_keys
		 WHERE idempotency_key = $1`,
		idempotencyKey,
	).Scan(
		&storedStatus,
		&storedHTTPStatus,
		&storedBody,
	)
	if err != nil {
		t.Fatalf("read stored idempotency response: %v", err)
	}

	if storedStatus != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s", storedStatus)
	}

	if storedHTTPStatus != http.StatusCreated {
		t.Fatalf(
			"expected stored HTTP 201, got %d",
			storedHTTPStatus,
		)
	}

	t.Logf(
		"Step 3: Database commit confirmed, payment_id=%s",
		firstPaymentID,
	)

	// ------------------------------------------------
	// 6. Retry the same request normally
	// ------------------------------------------------

	t.Log("Step 4: Retrying with the same idempotency key")

	retryClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	retryResp, err := retryClient.Do(newPaymentRequest())
	if err != nil {
		t.Fatalf("retry request failed: %v", err)
	}
	defer retryResp.Body.Close()

	retryBody, err := io.ReadAll(retryResp.Body)
	if err != nil {
		t.Fatalf("read retry response: %v", err)
	}

	if retryResp.StatusCode != http.StatusCreated {
		t.Fatalf(
			"expected HTTP 201, got %d: %s",
			retryResp.StatusCode,
			retryBody,
		)
	}

	if retryResp.Header.Get("Idempotency-Replayed") != "true" {
		t.Error("expected Idempotency-Replayed: true")
	}

	// Compare the response with the response committed
	// to the database during the first request.
	if !bytes.Equal(retryBody, []byte(storedBody)) {
		t.Errorf(
			"replayed response differs from stored response\n"+
				"stored: %s\nreplay: %s",
			storedBody,
			retryBody,
		)
	}

	var replayedPayment Payment

	if err := json.Unmarshal(
		retryBody,
		&replayedPayment,
	); err != nil {
		t.Fatalf("decode replayed payment: %v", err)
	}

	if replayedPayment.ID != firstPaymentID {
		t.Errorf(
			"payment ID changed: original=%s replayed=%s",
			firstPaymentID,
			replayedPayment.ID,
		)
	}

	// ------------------------------------------------
	// 7. Verify no duplicate payment was created
	// ------------------------------------------------

	var paymentCount int

	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM payments
		 WHERE order_id = $1`,
		orderID,
	).Scan(&paymentCount)
	if err != nil {
		t.Fatalf("count payments: %v", err)
	}

	var keyCount int

	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM idempotency_keys
		 WHERE idempotency_key = $1`,
		idempotencyKey,
	).Scan(&keyCount)
	if err != nil {
		t.Fatalf("count idempotency records: %v", err)
	}

	t.Log(fmt.Sprintf("Payment records: %d", paymentCount))
	t.Log(fmt.Sprintf("Idempotency records: %d", keyCount))
	t.Log("Retry returned the original committed response")

	if paymentCount != 1 {
		t.Errorf("expected 1 payment, got %d", paymentCount)
	}

	if keyCount != 1 {
		t.Errorf("expected 1 idempotency record, got %d", keyCount)
	}
}
