package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func TestConcurrentIdempotentPayments(t *testing.T) {
	// Load local environment configuration.
	// This path assumes the test runs from the example directory.
	if err := godotenv.Load("../../.env"); err != nil {
		t.Log("No .env file found; using OS environment")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		30*time.Second,
	)
	defer cancel()

	// Initialize PostgreSQL connection pool.
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("failed to create database pool: %v", err)
	}

	// Register pool cleanup FIRST.
	// t.Cleanup executes callbacks in LIFO order,
	// so the pool closes after test-data cleanup.
	t.Cleanup(func() {
		db.Close()
	})

	if err := db.Ping(ctx); err != nil {
		t.Fatalf("database connection failed: %v", err)
	}

	// Wire dependencies.
	repo := NewPostgresPaymentRepository(db)
	service := NewPaymentService(repo)
	handler := NewPaymentHandler(service)

	// Start an isolated HTTP test server.
	server := httptest.NewServer(
		http.HandlerFunc(handler.CreatePayment),
	)
	defer server.Close()

	// Unique identifiers isolate every test execution.
	orderID := uuid.New()
	idempotencyKey := "test-" + uuid.NewString()

	// Register data cleanup AFTER pool cleanup.
	// This executes before db.Close().
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()

		// Remove the idempotency record created by this test.
		_, err := db.Exec(
			cleanupCtx,
			`DELETE FROM idempotency_keys
			 WHERE idempotency_key = $1`,
			idempotencyKey,
		)
		if err != nil {
			t.Errorf(
				"cleanup idempotency key failed: %v",
				err,
			)
		}

		// Remove payment records associated with this test.
		_, err = db.Exec(
			cleanupCtx,
			`DELETE FROM payments
			 WHERE order_id = $1`,
			orderID,
		)
		if err != nil {
			t.Errorf(
				"cleanup payments failed: %v",
				err,
			)
		}
	})

	const concurrentRequests = 10

	type requestResult struct {
		statusCode int
		body       []byte
		replayed   bool
		err        error
	}

	results := make(chan requestResult, concurrentRequests)

	client := &http.Client{
		Timeout: 20 * time.Second,
	}

	payload, err := json.Marshal(CreatePaymentRequest{
		OrderID: orderID.String(),
		Amount:  1000,
	})
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var wg sync.WaitGroup

	// Fire 10 concurrent requests using the same
	// idempotency key and identical payload.
	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			req, err := http.NewRequest(
				http.MethodPost,
				server.URL,
				bytes.NewReader(payload),
			)
			if err != nil {
				results <- requestResult{err: err}
				return
			}

			req.Header.Set(
				"Content-Type",
				"application/json",
			)
			req.Header.Set(
				"Idempotency-Key",
				idempotencyKey,
			)

			resp, err := client.Do(req)
			if err != nil {
				results <- requestResult{err: err}
				return
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				results <- requestResult{err: err}
				return
			}

			results <- requestResult{
				statusCode: resp.StatusCode,
				body:       body,
				replayed: resp.Header.Get(
					"Idempotency-Replayed",
				) == "true",
			}
		}()
	}

	// Wait for every request to complete.
	wg.Wait()
	close(results)

	var originalBody []byte
	var responseCount int
	var replayedCount int
	var createdCount int

	for result := range results {
		if result.err != nil {
			t.Errorf("HTTP request failed: %v", result.err)
			continue
		}

		responseCount++

		if result.statusCode != http.StatusCreated {
			t.Errorf(
				"expected HTTP 201, got %d; response: %s",
				result.statusCode,
				string(result.body),
			)
			continue
		}

		// All responses must reference the same payment.
		if originalBody == nil {
			originalBody = append([]byte(nil), result.body...)
		} else if !bytes.Equal(originalBody, result.body) {
			t.Errorf(
				"response mismatch:\noriginal: %s\nreceived: %s",
				string(originalBody),
				string(result.body),
			)
		}

		if result.replayed {
			replayedCount++
		} else {
			createdCount++
		}
	}

	// Verify every request received a response.
	if responseCount != concurrentRequests {
		t.Errorf(
			"expected %d responses, got %d",
			concurrentRequests,
			responseCount,
		)
	}

	// Query PostgreSQL to verify the actual business state.
	var paymentCount int

	err = db.QueryRow(
		ctx,
		`SELECT COUNT(*)
		 FROM payments
		 WHERE order_id = $1`,
		orderID,
	).Scan(&paymentCount)
	if err != nil {
		t.Fatalf("failed to count payments: %v", err)
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
		t.Fatalf("failed to count idempotency keys: %v", err)
	}

	// Confirm that the idempotency operation completed.
	var storedStatus string
	var storedResponseStatus int

	err = db.QueryRow(
		ctx,
		`SELECT status, response_status
		 FROM idempotency_keys
		 WHERE idempotency_key = $1`,
		idempotencyKey,
	).Scan(
		&storedStatus,
		&storedResponseStatus,
	)
	if err != nil {
		t.Fatalf(
			"failed to fetch idempotency record: %v",
			err,
		)
	}

	// Log results for GitHub documentation.
	t.Log(fmt.Sprintf(
		"Concurrent requests: %d",
		concurrentRequests,
	))
	t.Log(fmt.Sprintf(
		"Payment records: %d",
		paymentCount,
	))
	t.Log(fmt.Sprintf(
		"Idempotency records: %d",
		keyCount,
	))
	t.Log(fmt.Sprintf(
		"Original responses: %d",
		createdCount,
	))
	t.Log(fmt.Sprintf(
		"Replayed responses: %d",
		replayedCount,
	))
	t.Log(fmt.Sprintf(
		"Idempotency status: %s",
		storedStatus,
	))

	// Business invariant: exactly one payment.
	if paymentCount != 1 {
		t.Errorf(
			"expected exactly 1 payment, got %d",
			paymentCount,
		)
	}

	// Exactly one idempotency record.
	if keyCount != 1 {
		t.Errorf(
			"expected exactly 1 idempotency record, got %d",
			keyCount,
		)
	}

	// One original execution and nine replays.
	if createdCount != 1 {
		t.Errorf(
			"expected 1 original response, got %d",
			createdCount,
		)
	}

	if replayedCount != concurrentRequests-1 {
		t.Errorf(
			"expected %d replayed responses, got %d",
			concurrentRequests-1,
			replayedCount,
		)
	}

	if storedStatus != "COMPLETED" {
		t.Errorf(
			"expected COMPLETED status, got %s",
			storedStatus,
		)
	}

	if storedResponseStatus != http.StatusCreated {
		t.Errorf(
			"expected stored HTTP 201, got %d",
			storedResponseStatus,
		)
	}
}
