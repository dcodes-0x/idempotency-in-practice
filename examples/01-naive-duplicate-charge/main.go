package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	// Load local environment variables.
	// Existing OS environment variables take precedence.
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found; using OS environment variables")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	appPort := os.Getenv("APP_PORT")
	if appPort == "" {
		appPort = "8080"
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Failed to initialize database pool: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}

	repo := NewPostgresPaymentRepository(db)
	service := NewPaymentService(repo)
	handler := NewPaymentHandler(service)

	mux := http.NewServeMux()
	mux.HandleFunc("/payments", handler.CreatePayment)

	log.Printf("Payment API running on :%s", appPort)

	server := &http.Server{
		Addr:              ":" + appPort,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Fatal(server.ListenAndServe())
}
