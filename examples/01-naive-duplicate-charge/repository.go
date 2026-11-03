package main

import (
	"context"

	"github.com/google/uuid"
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

func (r *PostgresPaymentRepository) Create(
	ctx context.Context,
	orderID uuid.UUID,
	amount int64,
) (Payment, error) {
	var payment Payment

	err := r.db.QueryRow(
		ctx,
		`INSERT INTO payments (order_id, amount)
         VALUES ($1, $2)
         RETURNING id, order_id, amount, status`,
		orderID,
		amount,
	).Scan(
		&payment.ID,
		&payment.OrderID,
		&payment.Amount,
		&payment.Status,
	)

	return payment, err
}
