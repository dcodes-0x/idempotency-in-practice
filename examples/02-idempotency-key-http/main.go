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
	if err := godotenv.Load(); err != nil {
		log.Println("No .env found; using OS environment")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Unable to initialize database: %v", err)
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

	port := os.Getenv("IDEMPOTENT_APP_PORT")
	if port == "" {
		port = "8081"
	}

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Idempotent payment API running on :%s", port)
	log.Fatal(server.ListenAndServe())
}
