package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// setupEdgeCaseTest initializes a real PostgreSQL-backed
// HTTP test server and registers cleanup callbacks.
//
// Tests use unique order IDs and keys to avoid collisions.
func setupEdgeCaseTest(t *testing.T) (
	*pgxpool.Pool,
	*httptest.Server,
	uuid.UUID,
	string,
) {
	t.Helper()

	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	// Register first: pool must close last.
	t.Cleanup(db.Close)

	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	repo := NewPostgresPaymentRepository(db)
	service := NewPaymentService(repo)
	handler := NewPaymentHandler(service)

	server := httptest.NewServer(
		http.HandlerFunc(handler.CreatePayment),
	)
	t.Cleanup(server.Close)

	orderID := uuid.New()
	key := "edge-case-" + uuid.NewString()

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()

		if _, err := db.Exec(
			cleanupCtx,
			`DELETE FROM idempotency_keys
			 WHERE idempotency_key = $1`,
			key,
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

	return db, server, orderID, key
}

// sendTestPayment sends a request and returns the HTTP
// response, including its body, for assertions.
func sendTestPayment(
	t *testing.T,
	server *httptest.Server,
	key string,
	orderID uuid.UUID,
	amount int64,
) (int, []byte) {
	t.Helper()

	body, err := json.Marshal(CreatePaymentRequest{
		OrderID: orderID.String(),
		Amount:  amount,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/payments",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, responseBody
}

func TestSameKeyDifferentPayload(t *testing.T) {
	db, server, orderID, key := setupEdgeCaseTest(t)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	// First request: amount = 1000.
	firstStatus, firstBody := sendTestPayment(
		t, server, key, orderID, 1000,
	)

	if firstStatus != http.StatusCreated {
		t.Fatalf(
			"expected first request HTTP 201, got %d: %s",
			firstStatus,
			firstBody,
		)
	}

	// Second request: same key, different amount.
	secondStatus, secondBody := sendTestPayment(
		t, server, key, orderID, 2000,
	)

	if secondStatus != http.StatusUnprocessableEntity {
		t.Fatalf(
			"expected HTTP 422, got %d: %s",
			secondStatus,
			secondBody,
		)
	}

	// The rejected request must not create a payment.
	var count int
	var storedAmount int64

	err := db.QueryRow(
		ctx,
		`SELECT COUNT(*), COALESCE(MAX(amount), 0)
		 FROM payments
		 WHERE order_id = $1`,
		orderID,
	).Scan(&count, &storedAmount)
	if err != nil {
		t.Fatal(err)
	}

	if count != 1 {
		t.Errorf("expected 1 payment, got %d", count)
	}

	if storedAmount != 1000 {
		t.Errorf(
			"expected stored amount 1000, got %d",
			storedAmount,
		)
	}

	var keyCount int

	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM idempotency_keys
		 WHERE idempotency_key = $1`,
		key,
	).Scan(&keyCount)
	if err != nil {
		t.Fatal(err)
	}

	if keyCount != 1 {
		t.Errorf(
			"expected 1 idempotency record, got %d",
			keyCount,
		)
	}

	t.Log("Original request: HTTP 201")
	t.Log("Conflicting request: HTTP 422")
	t.Logf("Payment records: %d", count)
	t.Logf("Stored amount: %d", storedAmount)
}

func TestRollbackAllowsRetry(t *testing.T) {
	db, server, orderID, key := setupEdgeCaseTest(t)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	// Phase 1: Begin a transaction and claim the key.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	_, err = tx.Exec(
		ctx,
		`INSERT INTO idempotency_keys (
			idempotency_key,
			request_hash,
			status
		)
		VALUES ($1, $2, 'IN_PROGRESS')`,
		key,
		"test-request-hash",
	)
	if err != nil {
		t.Fatalf("claim key: %v", err)
	}

	// Deliberately violate the payments amount constraint.
	// This simulates a database operation failing after
	// the idempotency key was claimed.
	_, err = tx.Exec(
		ctx,
		`INSERT INTO payments (order_id, amount)
		 VALUES ($1, $2)`,
		orderID,
		-100,
	)

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf(
			"expected PostgreSQL CHECK violation 23514, got %v",
			err,
		)
	}

	// PostgreSQL transaction is now aborted.
	// Roll back the entire operation.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback transaction: %v", err)
	}

	t.Log("First transaction failed and rolled back")

	// Verify neither the key nor the payment committed.
	var keyCount int

	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM idempotency_keys
		 WHERE idempotency_key = $1`,
		key,
	).Scan(&keyCount)
	if err != nil {
		t.Fatal(err)
	}

	if keyCount != 0 {
		t.Fatalf(
			"expected no committed key after rollback, got %d",
			keyCount,
		)
	}

	var paymentCount int

	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM payments
		 WHERE order_id = $1`,
		orderID,
	).Scan(&paymentCount)
	if err != nil {
		t.Fatal(err)
	}

	if paymentCount != 0 {
		t.Fatalf(
			"expected no payment after rollback, got %d",
			paymentCount,
		)
	}

	// Phase 2: Retry with the SAME key through the API.
	status, body := sendTestPayment(
		t, server, key, orderID, 1000,
	)

	if status != http.StatusCreated {
		t.Fatalf(
			"expected successful retry HTTP 201, got %d: %s",
			status,
			body,
		)
	}

	// Verify the successful retry created one payment.
	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM payments
		 WHERE order_id = $1`,
		orderID,
	).Scan(&paymentCount)
	if err != nil {
		t.Fatal(err)
	}

	if paymentCount != 1 {
		t.Errorf(
			"expected 1 payment after retry, got %d",
			paymentCount,
		)
	}

	var finalStatus string

	err = db.QueryRow(
		ctx,
		`SELECT status
		 FROM idempotency_keys
		 WHERE idempotency_key = $1`,
		key,
	).Scan(&finalStatus)
	if err != nil {
		t.Fatal(err)
	}

	if finalStatus != "COMPLETED" {
		t.Errorf(
			"expected COMPLETED, got %s",
			finalStatus,
		)
	}

	t.Log("Retry successfully claimed the same key")
	t.Logf("Payment records: %d", paymentCount)
	t.Logf("Idempotency status: %s", finalStatus)
}
