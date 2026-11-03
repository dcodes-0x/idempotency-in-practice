package main

import "github.com/google/uuid"

type CreatePaymentInput struct {
	OrderID uuid.UUID
	Amount  int64
}

type Payment struct {
	ID      uuid.UUID `json:"payment_id"`
	OrderID uuid.UUID `json:"order_id"`
	Amount  int64     `json:"amount"`
	Status  string    `json:"status"`
}

type PaymentResult struct {
	StatusCode int
	Body       []byte
	Replayed   bool
}
