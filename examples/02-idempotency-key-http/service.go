package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrInvalidAmount       = errors.New("amount must be positive")
	ErrInvalidKey          = errors.New("invalid idempotency key")
	ErrKeyConflict         = errors.New("idempotency key reused with different payload")
	ErrOperationInProgress = errors.New("operation is currently being processed")
)

type PaymentExecutor interface {
	Execute(
		ctx context.Context,
		key string,
		requestHash string,
		input CreatePaymentInput,
	) (PaymentResult, error)
}

type PaymentService struct {
	executor PaymentExecutor
}

func NewPaymentService(executor PaymentExecutor) *PaymentService {
	return &PaymentService{executor: executor}
}

func (s *PaymentService) CreatePayment(
	ctx context.Context,
	key string,
	input CreatePaymentInput,
) (PaymentResult, error) {
	if len(key) == 0 || len(key) > 255 {
		return PaymentResult{}, ErrInvalidKey
	}

	if input.OrderID == uuid.Nil {
		return PaymentResult{}, errors.New("order_id is required")
	}

	if input.Amount <= 0 {
		return PaymentResult{}, ErrInvalidAmount
	}

	// Hash typed, normalized business input.
	payload, err := json.Marshal(input)
	if err != nil {
		return PaymentResult{}, err
	}

	sum := sha256.Sum256(payload)
	requestHash := hex.EncodeToString(sum[:])

	return s.executor.Execute(ctx, key, requestHash, input)
}
