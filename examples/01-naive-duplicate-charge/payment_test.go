package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConcurrentDuplicatePayments(t *testing.T) {
	databaseURL := "postgres://dev:devpassword@localhost:55432/idempotency?sslmode=disable"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	repo := NewPostgresPaymentRepository(db)
	service := NewPaymentService(repo)
	handler := NewPaymentHandler(service)

	server := httptest.NewServer(http.HandlerFunc(handler.CreatePayment))
	defer server.Close()

	orderID := uuid.New()
	const concurrentRequests = 10

	var wg sync.WaitGroup
	errors := make(chan error, concurrentRequests)

	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			body := fmt.Sprintf(
				`{"order_id":"%s","amount":1000}`,
				orderID,
			)

			resp, err := http.Post(
				server.URL,
				"application/json",
				strings.NewReader(body),
			)
			if err != nil {
				errors <- err
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusCreated {
				errors <- fmt.Errorf(
					"unexpected status: %d",
					resp.StatusCode,
				)
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	var count int

	err = db.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM payments WHERE order_id = $1",
		orderID,
	).Scan(&count)

	if err != nil {
		t.Fatal(err)
	}

	t.Logf("Concurrent requests: %d", concurrentRequests)
	t.Logf("Payment records created: %d", count)

	// Intentionally demonstrate the duplicate creation problem.
	if count != concurrentRequests {
		t.Errorf(
			"expected %d duplicate records, got %d",
			concurrentRequests,
			count,
		)
	}
}
