package main

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestIdempotencyKeyLockContention(t *testing.T) {
	db, server, orderID, key := setupEdgeCaseTest(t)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	defer cancel()

	// Step 1: Begin transaction A.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction A: %v", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	// Claim the idempotency key but do not commit.
	_, err = tx.Exec(ctx, `
		INSERT INTO idempotency_keys (
			idempotency_key,
			request_hash,
			status
		)
		VALUES ($1, $2, 'IN_PROGRESS')
	`, key, "uncommitted-request-hash")

	if err != nil {
		t.Fatalf("claim key in transaction A: %v", err)
	}

	t.Log("Transaction A claimed key without committing")

	// Step 2: Request B attempts to use the same key.
	// Its INSERT should wait for transaction A.
	start := time.Now()

	status, body := sendTestPayment(
		t,
		server,
		key,
		orderID,
		1000,
	)

	elapsed := time.Since(start)

	// Request B should stop waiting after lock_timeout.
	if status != http.StatusConflict {
		t.Fatalf(
			"expected HTTP 409, got %d: %s",
			status,
			body,
		)
	}

	t.Logf("Request B returned HTTP 409 after %s", elapsed)

	// A generous upper bound guards against indefinite
	// waiting without requiring precise scheduler timing.
	if elapsed > 5*time.Second {
		t.Errorf(
			"request took too long: %s",
			elapsed,
		)
	}

	// Step 3: Verify request B created no payment.
	var paymentCount int

	err = db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM payments
		WHERE order_id = $1
	`, orderID).Scan(&paymentCount)

	if err != nil {
		t.Fatalf("count payments: %v", err)
	}

	if paymentCount != 0 {
		t.Fatalf(
			"expected 0 payments while key is locked, got %d",
			paymentCount,
		)
	}

	// Step 4: Roll back transaction A.
	// Its uncommitted claim should disappear.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback transaction A: %v", err)
	}

	t.Log("Transaction A rolled back")

	// Step 5: Retry the same HTTP operation.
	// The key is now available.
	retryStatus, retryBody := sendTestPayment(
		t,
		server,
		key,
		orderID,
		1000,
	)

	if retryStatus != http.StatusCreated {
		t.Fatalf(
			"expected HTTP 201 after rollback, got %d: %s",
			retryStatus,
			retryBody,
		)
	}

	// Step 6: Verify final database state.
	err = db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM payments
		WHERE order_id = $1
	`, orderID).Scan(&paymentCount)

	if err != nil {
		t.Fatalf("count payments after retry: %v", err)
	}

	if paymentCount != 1 {
		t.Errorf(
			"expected exactly 1 payment, got %d",
			paymentCount,
		)
	}

	var keyCount int
	var finalStatus string

	err = db.QueryRow(ctx, `
		SELECT COUNT(*), MAX(status)
		FROM idempotency_keys
		WHERE idempotency_key = $1
	`, key).Scan(&keyCount, &finalStatus)

	if err != nil {
		t.Fatalf("query final idempotency state: %v", err)
	}

	if keyCount != 1 {
		t.Errorf(
			"expected 1 idempotency record, got %d",
			keyCount,
		)
	}

	if finalStatus != "COMPLETED" {
		t.Errorf(
			"expected COMPLETED, got %s",
			finalStatus,
		)
	}

	t.Logf("Final payment records: %d", paymentCount)
	t.Logf("Final idempotency records: %d", keyCount)
	t.Logf("Final idempotency status: %s", finalStatus)
}
