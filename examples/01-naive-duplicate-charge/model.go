package main

import "github.com/google/uuid"

type Payment struct {
	ID      uuid.UUID
	OrderID uuid.UUID
	Amount  int64
	Status  string
}

type CreatePaymentInput struct {
	OrderID uuid.UUID
	Amount  int64
}
