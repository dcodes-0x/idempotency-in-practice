package main

import (
	"encoding/json"
	"errors"
	"net/http"

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
		http.Error(w, "Method not allowed",
			http.StatusMethodNotAllowed)
		return
	}

	var req CreatePaymentRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	orderID, err := uuid.Parse(req.OrderID)
	if err != nil {
		http.Error(w, "Invalid order_id", http.StatusBadRequest)
		return
	}

	payment, err := h.service.CreatePayment(
		r.Context(),
		CreatePaymentInput{
			OrderID: orderID,
			Amount:  req.Amount,
		},
	)

	if errors.Is(err, ErrInvalidAmount) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err != nil {
		http.Error(w, "Unable to create payment",
			http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"payment_id": payment.ID,
		"order_id":   payment.OrderID,
		"amount":     payment.Amount,
		"status":     payment.Status,
	})
}
