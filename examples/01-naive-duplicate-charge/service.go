package main

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var ErrInvalidAmount = errors.New("amount must be positive")

type PaymentStore interface {
	Create(
		ctx context.Context,
		orderID uuid.UUID,
		amount int64,
	) (Payment, error)
}

type PaymentService struct {
	repo PaymentStore
}

func NewPaymentService(repo PaymentStore) *PaymentService {
	return &PaymentService{repo: repo}
}

func (s *PaymentService) CreatePayment(
	ctx context.Context,
	input CreatePaymentInput,
) (Payment, error) {
	if input.Amount <= 0 {
		return Payment{}, ErrInvalidAmount
	}

	return s.repo.Create(
		ctx,
		input.OrderID,
		input.Amount,
	)
}
