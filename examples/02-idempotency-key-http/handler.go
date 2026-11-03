package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type CreatePaymentRequest struct {
	OrderID string `json:"order_id"`
	Amount  int64  `json:"amount"`
}

type PaymentHandler struct {
	service *PaymentService
}

func NewPaymentHandler(service *PaymentService) *PaymentHandler {
	return &PaymentHandler{service: service}
}

func (h *PaymentHandler) CreatePayment(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	if key == "" || len(key) > 255 {
		http.Error(
			w,
			"Valid Idempotency-Key header is required",
			http.StatusBadRequest,
		)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req CreatePaymentRequest

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "Expected a single JSON object", http.StatusBadRequest)
		return
	}

	orderID, err := uuid.Parse(req.OrderID)
	if err != nil {
		http.Error(w, "Invalid order_id", http.StatusBadRequest)
		return
	}

	result, err := h.service.CreatePayment(
		r.Context(),
		key,
		CreatePaymentInput{
			OrderID: orderID,
			Amount:  req.Amount,
		},
	)

	switch {
	case errors.Is(err, ErrInvalidAmount):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return

	case errors.Is(err, ErrInvalidKey):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return

	case errors.Is(err, ErrKeyConflict):
		http.Error(
			w,
			"Idempotency key reused with a different payload",
			http.StatusUnprocessableEntity,
		)
		return

	case errors.Is(err, ErrOperationInProgress):
		w.Header().Set("Retry-After", "1")

		http.Error(
			w,
			"Operation is still being processed. Retry with the same idempotency key.",
			http.StatusConflict,
		)
		return

	case err != nil:
		log.Printf("Payment creation failed: %v", err)

		http.Error(
			w,
			"Internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if result.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}

	w.WriteHeader(result.StatusCode)
	_, _ = w.Write(result.Body)
}
