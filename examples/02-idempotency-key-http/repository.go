package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresPaymentRepository struct {
	db *pgxpool.Pool
}

func NewPostgresPaymentRepository(
	db *pgxpool.Pool,
) *PostgresPaymentRepository {
	return &PostgresPaymentRepository{db: db}
}

func (r *PostgresPaymentRepository) Execute(
	ctx context.Context,
	key string,
	requestHash string,
	input CreatePaymentInput,
) (PaymentResult, error) {

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PaymentResult{}, err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		SET LOCAL lock_timeout = '500ms'
	`)
	if err != nil {
		return PaymentResult{}, err
	}

	// Claim ownership of this logical operation.
	var claimedKey string

	err = tx.QueryRow(ctx, `
        INSERT INTO idempotency_keys (
            idempotency_key,
            request_hash,
            status
        )
        VALUES ($1, $2, 'IN_PROGRESS')
        ON CONFLICT (idempotency_key) DO NOTHING
        RETURNING idempotency_key
    `, key, requestHash).Scan(&claimedKey)

	if err != nil {
		var pgErr *pgconn.PgError

		// PostgreSQL SQLSTATE 55P03:
		// lock_not_available, including lock_timeout.
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return PaymentResult{}, ErrOperationInProgress
		}

		if !errors.Is(err, pgx.ErrNoRows) {
			return PaymentResult{}, err
		}
	}

	// Existing key: replay the original operation.
	if errors.Is(err, pgx.ErrNoRows) {
		var storedHash, status string
		var responseStatus int
		var responseBody string

		err = tx.QueryRow(ctx, `
            SELECT request_hash, status,
                   response_status, response_body
            FROM idempotency_keys
            WHERE idempotency_key = $1
        `, key).Scan(
			&storedHash,
			&status,
			&responseStatus,
			&responseBody,
		)
		if err != nil {
			return PaymentResult{}, err
		}

		if storedHash != requestHash {
			return PaymentResult{}, ErrKeyConflict
		}

		if status != "COMPLETED" {
			return PaymentResult{}, fmt.Errorf(
				"unexpected idempotency status: %s", status,
			)
		}

		return PaymentResult{
			StatusCode: responseStatus,
			Body:       []byte(responseBody),
			Replayed:   true,
		}, nil
	}

	// New key: perform the business operation.
	var payment Payment

	err = tx.QueryRow(ctx, `
        INSERT INTO payments (order_id, amount)
        VALUES ($1, $2)
        RETURNING id, order_id, amount, status
    `, input.OrderID, input.Amount).Scan(
		&payment.ID,
		&payment.OrderID,
		&payment.Amount,
		&payment.Status,
	)
	if err != nil {
		return PaymentResult{}, err
	}

	body, err := json.Marshal(payment)
	if err != nil {
		return PaymentResult{}, err
	}

	// Save the original response atomically.
	_, err = tx.Exec(ctx, `
        UPDATE idempotency_keys
        SET status = 'COMPLETED',
            response_status = $2,
            response_body = $3,
            updated_at = NOW()
        WHERE idempotency_key = $1
    `, key, http.StatusCreated, string(body))
	if err != nil {
		return PaymentResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return PaymentResult{}, err
	}

	return PaymentResult{
		StatusCode: http.StatusCreated,
		Body:       body,
		Replayed:   false,
	}, nil
}
