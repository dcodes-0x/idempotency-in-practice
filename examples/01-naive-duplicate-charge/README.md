# 01 — Naive Duplicate Payment

**What happens when a payment request is executed multiple times without idempotency protection?**

This example demonstrates how duplicate HTTP requests can create multiple payment records for a single logical operation.

The implementation is intentionally non-idempotent. It serves as the baseline for understanding why retry-safe APIs matter in distributed systems.

## The Problem

Consider a payment API:

```http
POST /payments
```

A client sends a request to create a payment. The backend processes the request, inserts a payment record into PostgreSQL, and returns a successful response.

Now consider this failure scenario:

1. The client initiates a payment.
2. The server successfully commits the payment to PostgreSQL.
3. The response is lost due to a network timeout.
4. The client cannot determine whether the payment succeeded.
5. The client retries the same request.
6. The backend creates another payment record.

**One logical payment operation has now produced two payment records.**

This happens because the backend treats every incoming request as a new operation.

### Failure Timeline

```mermaid
sequenceDiagram
    participant C as Client
    participant API as Payment API
    participant DB as PostgreSQL

    C->>API: POST /payments (order_id=A)
    API->>DB: INSERT payment
    DB-->>API: Payment ID = P1 (committed)
    Note over C,API: Response lost / client timeout
    C->>API: Retry POST /payments (order_id=A)
    API->>DB: INSERT payment
    DB-->>API: Payment ID = P2 (committed)
    API-->>C: 201 Created (P2)

    Note over DB: Two payment records for one order
```

## Tech Stack

- **Go** — HTTP server and application logic
- **PostgreSQL** — Persistent payment records
- **pgx/v5** — PostgreSQL driver and connection pooling
- **Docker Compose** — Local database infrastructure
- **Go testing** — Concurrent integration testing

## Architecture

The application follows a layered architecture:

```text
HTTP Client
    |
    v
Payment Handler
    |
    v
Payment Service
    |
    v
Payment Repository
    |
    v
PostgreSQL
```

| Layer | Responsibility |
|---|---|
| Handler | HTTP request parsing, validation, and response serialization |
| Service | Payment business rules and orchestration |
| Repository | PostgreSQL persistence |
| Database | Payment storage |

Although the code is organized into layers, **layered architecture does not automatically guarantee idempotency**.

## Project Structure

```text
01-naive-duplicate-charge/
├── main.go
├── handler.go
├── service.go
├── repository.go
├── model.go
├── payment_test.go
└── README.md
```

## Database Design

The `payments` table uses UUIDs for payment and order identifiers.

```sql
CREATE TABLE payments (
                          id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                          order_id UUID NOT NULL,
                          amount BIGINT NOT NULL CHECK (amount > 0),
                          status VARCHAR(30) NOT NULL DEFAULT 'SUCCESS',
                          created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

The `order_id` column is deliberately **not unique**.

This allows multiple payment records to be inserted for the same order, making the duplicate-operation problem observable.

The amount is stored as an integer representing the smallest currency unit to avoid floating-point precision issues.

## Running the Example

All commands below should be executed from the repository root.

### 1. Start PostgreSQL

Ensure Docker Desktop is running.

```bash
docker compose up -d
```

Verify the database:

```bash
docker compose ps
```

### 2. Configure Environment Variables

Create a `.env` file in the repository root.

```dotenv
APP_PORT=8080
APP_ENV=development
DATABASE_URL=postgres://dev:devpassword@localhost:55432/idempotency?sslmode=disable
```

The port and credentials above correspond to the local development setup. Adjust them if your Docker Compose configuration differs.

Do not commit `.env` files containing credentials.

### 3. Apply Database Migration

On Windows PowerShell:

```powershell
Get-Content .\migrations\00001_create_payments.up.sql |
    docker exec -i idempotency-postgres psql -v ON_ERROR_STOP=1 -U dev -d idempotency
```

### 4. Run the Payment API

```bash
go run ./examples/01-naive-duplicate-charge
```

The application starts on:

```text
http://localhost:8080
```

## Reproducing the Bug

### Send a Payment Request

```bash
curl --location 'http://localhost:8080/payments' \
--header 'Content-Type: application/json' \
--data '{
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000
}'
```

Example response:

```json
{
  "payment_id": "a84e25b7-3c91-4a0e-9d56-2d72e4a98c12",
  "order_id": "550e8400-e29b-41d4-a716-446655440000",
  "amount": 1000,
  "status": "SUCCESS"
}
```

Response status: `201 Created`.

### Retry the Same Request

Send the exact same request again without changing the order ID or amount.

The API returns another `201 Created` response with a **different payment ID**.

Both requests are accepted independently.

### Verify Duplicate Records

Execute:

```sql
SELECT
    order_id,
    COUNT(*) AS payment_count,
    SUM(amount) AS total_amount
FROM payments
WHERE order_id = '550e8400-e29b-41d4-a716-446655440000'
GROUP BY order_id;
```

After two successful requests, the expected result is:

| payment_count | total_amount |
|---|---|
| 2 | 2000 |

The API has persisted two payment records for the same logical payment operation.

**Note:** This example simulates payment creation in a database. It does not initiate actual financial charges through a payment provider.

## Concurrent Request Testing

Duplicate operations are not limited to sequential retries.

They can also occur when multiple requests arrive concurrently.

The integration test sends 10 HTTP requests for the same `order_id` using Go goroutines.

```text
              10 concurrent requests
                       |
         +-------------+-------------+
         |             |             |
      Request 1     Request 2     Request N
         |             |             |
         +-------------+-------------+
                       |
                 Payment API
                       |
                  PostgreSQL
                       |
                 10 payment rows
```

### Run the Test

```bash
go test -v ./examples/01-naive-duplicate-charge -run TestConcurrentDuplicatePayments -count=1
```

### Expected Result

```text
=== RUN   TestConcurrentDuplicatePayments
    Concurrent requests: 10
    Payment records created: 10
--- PASS: TestConcurrentDuplicatePayments
PASS
```

The test intentionally passes when it reproduces the duplicate-payment behavior.

It demonstrates the failure mode, not the correctness of the business operation.

The test uses a fresh order UUID to isolate each run.

## Why Does This Happen?

The repository executes an unconditional insert:

```sql
INSERT INTO payments (order_id, amount)
VALUES ($1, $2)
RETURNING id, order_id, amount, status;
```

PostgreSQL generates a new payment UUID for every successful insert.

There is no check or constraint identifying repeated requests as the same logical operation.

Consequently:

- Every request can create a new payment record.
- UUID primary keys do not prevent logical duplicates.
- Database connection pooling does not provide idempotency.
- HTTP retries are not automatically safe.
- A properly structured handler-service-repository design does not prevent duplicate side effects.

## Engineering Takeaways

**1. A successful database commit does not guarantee the client receives the response.**

Network failures can leave clients uncertain about operation outcomes.

**2. Retries are necessary, but naive retries can be dangerous.**

Retry behavior must account for operations with side effects.

**3. Technical uniqueness is different from business uniqueness.**

A UUID uniquely identifies a payment record. It does not necessarily identify a logical payment attempt.

**4. Concurrency safety is not the same as idempotency.**

Even when individual database inserts are concurrency-safe, the overall business operation can still be non-idempotent.

**5. Idempotency must be designed explicitly.**

The application needs a reliable mechanism to identify repeated operations and prevent duplicate business effects.

## What's Next?

### 02 — HTTP Idempotency Keys

The next example introduces:

- An `Idempotency-Key` request header
- Request payload fingerprinting
- PostgreSQL uniqueness constraints
- Atomic transactions
- Response persistence and replay
- Safe concurrent retry handling
- Rejection of key reuse with a different payload

The objective is to transform the same concurrent test from:

```text
10 requests → 10 payments
```

Into:

```text
10 requests → 1 payment
```

All successful retries should reference the original payment.

---

Part of [Idempotency in Practice](../../README.md) — a hands-on exploration of idempotency and safe retries in backend systems.