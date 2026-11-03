# 02 — HTTP Idempotency Keys

**Making payment retries safe using Go, PostgreSQL transactions, and idempotency keys.**

In [Demo 01 — Naive Duplicate Payment](../01-naive-duplicate-charge/README.md), we demonstrated how repeated HTTP requests can create multiple payment records for the same logical operation.

This example implements a database-backed idempotency mechanism that allows clients to safely retry payment requests without producing duplicate payment records.

The core guarantee is:

**Multiple requests with the same idempotency key and equivalent payload produce one committed payment and return the same stored response.**

## 1. The Problem

Consider a payment creation API:

```http
POST /payments
```

A client sends a payment request. PostgreSQL commits the operation, but the HTTP response is lost because of a network timeout.

The client cannot determine whether the original request succeeded.

Retrying without idempotency protection may create another payment.

This is known as an **ambiguous outcome**: the caller doesn't know whether the operation committed.

### The Failure Scenario

```mermaid
sequenceDiagram
    participant C as Client
    participant API as Payment API
    participant DB as PostgreSQL

    C->>API: POST /payments
    API->>DB: INSERT payment
    DB-->>API: Payment committed (P1)
    Note over C,API: Response lost / timeout
    C->>API: Retry POST /payments
    API->>DB: INSERT payment
    DB-->>API: Another payment committed (P2)
    API-->>C: 201 Created (P2)
    Note over DB: Duplicate payment records
```

The problem is not that PostgreSQL executed the inserts incorrectly.

The problem is that the application failed to recognize repeated requests as the same logical operation.

## 2. The Solution — Idempotency Keys

An idempotency key is a client-provided identifier representing one logical operation.

The client generates a key before making the original request and reuses that key for subsequent retries.

Example:

```http
POST /payments HTTP/1.1
Content-Type: application/json
Idempotency-Key: payment-order-1001

{
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000
}
```

The server uses the key to identify whether the operation has already been processed.

| Request | Behavior |
|---|---|
| New key | Execute and persist the operation |
| Existing key, equivalent payload | Return the original stored response |
| Existing key, different payload | Reject with HTTP 422 |
| Concurrent requests with the same key | Only one transaction creates the payment |
| Transaction rolled back | A subsequent retry may execute the operation |

The key is not the payment ID.

The payment ID identifies a persisted payment record, whereas the idempotency key identifies a logical operation that may be attempted multiple times.

## 3. Architecture

This example uses a layered Go application:

```text
HTTP Client
    |
    | POST /payments
    | Idempotency-Key header
    v
Payment Handler
    |
    | Parse and validate request
    v
Payment Service
    |
    | Validate business input
    | Compute request fingerprint
    v
Payment Repository / Executor
    |
    | Begin transaction
    | Claim idempotency key
    | Create payment or replay response
    v
PostgreSQL
    |
    +-- payments
    |
    +-- idempotency_keys
```

### Layer Responsibilities

| Layer | Responsibility |
|---|---|
| Handler | HTTP parsing, header validation, response serialization |
| Service | Business validation and request fingerprinting |
| Repository | Atomic idempotency claim, payment persistence, response retrieval |
| PostgreSQL | Transactions, unique constraints, and durable storage |

The initial implementation keeps transaction ownership inside the repository executor because claiming the key, creating the payment, and persisting its response form one atomic database operation.

## 4. Project Structure

```text
02-idempotency-key-http/
├── main.go
├── handler.go
├── service.go
├── repository.go
├── model.go
├── idempotency_test.go
└── README.md
```

The example shares the repository's root Go module, Docker Compose configuration, and database migrations.

## 5. Database Design

The implementation uses two PostgreSQL tables.

### Payments

```sql
CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    status VARCHAR(30) NOT NULL DEFAULT 'SUCCESS',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Payment IDs and order IDs use PostgreSQL's native UUID type.

The `order_id` column is deliberately not unique, preserving the behavior demonstrated in Demo 01.

### Idempotency Keys

```sql
CREATE TABLE idempotency_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    request_hash VARCHAR(64) NOT NULL,

    status VARCHAR(20) NOT NULL DEFAULT 'IN_PROGRESS',

    response_status INTEGER,
    response_body TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT valid_idempotency_status
        CHECK (status IN ('IN_PROGRESS', 'COMPLETED'))
);
```

### Important Columns

| Column | Purpose |
|---|---|
| `idempotency_key` | Identifies the logical operation |
| `request_hash` | Detects reuse of a key with different input |
| `status` | Represents the operation's execution state |
| `response_status` | Stores the original HTTP status |
| `response_body` | Stores the serialized response for replay |

The unique constraint on `idempotency_key` is central to preventing multiple transactions from claiming the same operation.

In this introductory implementation, keys are globally unique. Production applications generally need key scoping by client, tenant, or API operation.

## 6. How Idempotent Execution Works

The implementation uses PostgreSQL transactions and `INSERT ... ON CONFLICT DO NOTHING`.

### Execution Flow

```mermaid
flowchart TD
    A[Receive payment request] --> B[Validate input and compute request hash]
    B --> C[Begin PostgreSQL transaction]
    C --> D[Attempt to insert idempotency key]
    D --> E{Was key claimed?}
    E -->|Yes| F[Create payment]
    F --> G[Store original HTTP response]
    G --> H[Commit transaction]
    H --> I[Return 201 Created]

    E -->|No| J[Read existing idempotency record]
    J --> K{Request hash matches?}
    K -->|No| L[Return 422]
    K -->|Yes| M[Return stored response]
```

### New Request

For a previously unseen key:

1. Start a transaction.
2. Insert the idempotency record.
3. Insert the payment record.
4. Store the serialized payment response.
5. Mark the key as `COMPLETED`.
6. Commit the transaction.
7. Return HTTP `201 Created`.

The payment and its saved response are committed atomically.

### Duplicate Request

For an existing key:

1. Attempt to claim the same key.
2. PostgreSQL detects the uniqueness conflict.
3. Read the existing record.
4. Compare the stored request hash with the incoming hash.
5. Return the original saved response if the hashes match.
6. Reject the request if the hashes differ.

The business operation is not executed again.

### Atomicity Guarantee

The key design decision is that the idempotency record, payment record, and saved response are written inside one transaction.

```text
BEGIN
  |
  +-- Claim idempotency key
  |
  +-- Insert payment
  |
  +-- Save response and mark COMPLETED
  |
COMMIT
```

If the transaction fails before commit, its database changes are rolled back.

If the transaction commits, the payment and its corresponding completed idempotency record are durable together.

This prevents a committed payment from being left without its idempotency result within the scope of this database transaction.

## 7. Handling Concurrent Requests

Now consider 10 requests arriving simultaneously with the same idempotency key.

All requests attempt to insert the same key into a column protected by a PostgreSQL unique constraint.

Only one transaction can successfully claim the key.

Competing transactions must wait for the conflicting transaction to commit or roll back.

### Concurrent Execution

```mermaid
sequenceDiagram
    participant A as Request A
    participant B as Request B
    participant DB as PostgreSQL

    A->>DB: BEGIN
    A->>DB: INSERT idempotency key K
    DB-->>A: Key claimed

    B->>DB: BEGIN
    B->>DB: INSERT key K ON CONFLICT DO NOTHING
    Note over B,DB: Wait for transaction A

    A->>DB: INSERT payment P1
    A->>DB: Save response
    A->>DB: COMMIT

    DB-->>B: Conflict resolved, no row inserted
    B->>DB: SELECT saved response for K
    DB-->>B: Original response for P1

    Note over A,B: Both receive the same payment ID
```

This behavior relies on PostgreSQL's uniqueness enforcement and transaction semantics.

The implementation uses the default `READ COMMITTED` isolation level. The conflicting insert and subsequent `SELECT` are separate statements, allowing the retrying transaction to observe the completed record after the original transaction commits.

### Why Not SELECT Before INSERT?

An unsafe implementation might perform:

```sql
SELECT *
FROM idempotency_keys
WHERE idempotency_key = $1;
```

And then:

```sql
INSERT INTO idempotency_keys (...);
```

Two concurrent requests could both observe that the key is absent before either request inserts it.

This creates a check-then-act race.

A unique constraint with an atomic insert operation avoids relying on that unsafe assumption.

## 8. Request Fingerprinting

An idempotency key must not be reused to silently execute a different operation.

For example, suppose the original request contains:

```json
{
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000
}
```

A retry arrives with the same key but an amount of `2000`.

The application computes a SHA-256 fingerprint of the normalized business input and compares it with the stored fingerprint.

If the fingerprints differ, the request is rejected.

```http
HTTP/1.1 422 Unprocessable Entity
```

The fingerprint is based on typed business input rather than raw JSON bytes.

This avoids treating differences in whitespace or JSON field ordering as different operations.

The chosen fingerprinting representation must remain stable when the request schema evolves.

## 9. Running the Example

All commands should be executed from the repository root.

### Prerequisites

- Go
- Docker Desktop
- PostgreSQL via Docker Compose
- Database migrations 001 and 002 applied

### Start PostgreSQL

```bash
docker compose up -d
```

Verify:

```bash
docker compose ps
```

### Configure Environment Variables

Create or update the root `.env`:

```dotenv
APP_PORT=8080
IDEMPOTENT_APP_PORT=8081
APP_ENV=development

DATABASE_URL=postgres://dev:devpassword@localhost:55432/idempotency?sslmode=disable
```

These credentials and ports are examples for the local development environment.

### Apply Database Migrations

If using `golang-migrate`, apply pending migrations:

```powershell
$env:DATABASE_URL = "postgres://dev:devpassword@localhost:55432/idempotency?sslmode=disable"

migrate -path ./migrations -database $env:DATABASE_URL up
```

If the schema was previously created manually, baseline the corresponding migration version only after verifying that the schemas match.

### Start the Server

```bash
go run ./examples/02-idempotency-key-http
```

Expected startup message:

```text
Idempotent payment API running on :8081
```

## 10. API Testing

### Test A — First Payment Request

```bash
curl --location 'http://localhost:8081/payments' \
--header 'Content-Type: application/json' \
--header 'Idempotency-Key: payment-order-1001' \
--data '{
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000
}'
```

Expected status: `201 Created`.

Example response:

```json
{
    "payment_id": "a84e25b7-3c91-4a0e-9d56-2d72e4a98c12",
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000,
    "status": "SUCCESS"
}
```

The UUID above is illustrative.

### Test B — Retry With the Same Key

Send the exact same request again.

Expected behavior:

- HTTP status remains `201 Created`.
- The original payment ID is returned.
- No additional payment is created.
- Response contains `Idempotency-Replayed: true`.

### Test C — Same Key, Different Payload

Keep the same key, but change the amount to `2000`.

Expected status: `422 Unprocessable Entity`.

The operation must not create another payment.

For independent test runs, generate a fresh idempotency key. Reusing an existing key intentionally replays its previous result.

## 11. Concurrent Integration Testing

The integration test sends 10 simultaneous HTTP requests using one shared idempotency key and identical payment input.

The database is then queried to verify the resulting state.

### Run the Test

```bash
go test -v ./examples/02-idempotency-key-http -run TestConcurrentIdempotentPayments -count=1
```

### Expected Output

```text
=== RUN   TestConcurrentIdempotentPayments
    Concurrent requests: 10
    Payment records: 1
    Idempotency records: 1
    Replayed responses: 9
--- PASS: TestConcurrentIdempotentPayments
PASS
```

This is the intended output when all requests succeed. Actual output should be verified by running the test.

### Assertions

The test verifies:

| Assertion | Expected |
|---|---|
| HTTP requests | 10 |
| Successful responses | 10 |
| Payment records | 1 |
| Idempotency records | 1 |
| Replayed responses | 9 |
| Response bodies | Identical |

The test also uses a fresh order UUID and idempotency key to isolate each execution.

Unlike the Go race detector, these integration assertions check database-level business behavior.

## 12. Demo 01 vs Demo 02

| Property | Demo 01 | Demo 02 |
|---|---|---|
| Duplicate requests | Independent inserts | Deduplicated by key |
| Payment records from 10 retries | 10 | 1 |
| Request fingerprinting | No | Yes |
| Response replay | No | Yes |
| PostgreSQL unique key coordination | No | Yes |
| Atomic idempotency and payment writes | No | Yes |
| Different payload with same key | Allowed as new request | Rejected |

The key improvement is not simply checking whether a payment already exists.

It is **establishing a durable relationship between a logical request, its execution, and its original response**.

## 13. Limitations and Trade-offs

This example focuses on one simulated payment operation stored in a single PostgreSQL database.

It is not a complete payment-processing system.

### Key Expiration

Idempotency keys are currently retained indefinitely.

Production systems need a retention policy that considers retry windows, storage growth, and the risk of delayed retries after expiration.

### Key Scoping

Keys are currently unique globally.

In multi-tenant systems, a composite key such as `(client_id, operation, idempotency_key)` is generally more appropriate.

### Concurrent Request Waiting

Concurrent requests may wait for a transaction holding the same key.

The current example does not implement an explicit application-level lock-wait timeout or return `409 Conflict` for in-progress operations.

Production implementations should bound waiting time and define retry behavior.

### External Side Effects

The payment operation in this demo consists only of a PostgreSQL insert.

A real payment gateway cannot generally participate in the same PostgreSQL transaction.

External calls require additional mechanisms, such as downstream idempotency keys, durable operation state, and reconciliation.

### Ambiguous Commit Outcomes

A database commit may succeed even if the application loses the connection before receiving confirmation.

A subsequent retry using the same idempotency key can retrieve the committed result, provided the database transaction completed successfully.

### Exactly-Once Semantics

Idempotency does not guarantee exactly-once request delivery or execution.

Instead, it can make repeated execution attempts produce an effectively-once business effect within a defined scope.

### Response Storage

Persisting response bodies introduces storage costs and may retain sensitive information.

Production systems should carefully control what is stored, retention duration, and access to persisted responses.

## 14. Engineering Takeaways

**1. Retry safety is a business-level guarantee.**

A valid HTTP request or successful database insert does not automatically mean repeated execution is safe.

**2. Idempotency requires durable coordination.**

Application memory alone is insufficient when requests may reach multiple server instances.

**3. Unique constraints are concurrency primitives.**

PostgreSQL can enforce atomic ownership of a logical operation under concurrent access.

**4. Transaction boundaries matter.**

Creating a payment and persisting its idempotency result must be coordinated atomically for this single-database design.

**5. Response replay handles ambiguous outcomes.**

Clients can retry after timeouts without needing to know whether the previous request committed.

**6. Request fingerprints prevent incorrect key reuse.**

The same idempotency key must not silently represent different business operations.

**7. Idempotency and ordering are separate concerns.**

Preventing duplicate operations does not ensure that distinct operations execute in the correct order.

## 15. What's Next?

The next example explores database-level uniqueness using PostgreSQL constraints and `ON CONFLICT`.

Rather than storing request-specific responses, it demonstrates how a business invariant can sometimes be enforced directly by the database.

[03 — Database Constraints and Upsert](../03-db-constraint-upsert/README.md) *(planned)*

---

Part of [Idempotency in Practice](../../README.md) — a hands-on exploration of retries, concurrency, and reliable backend execution using Go and PostgreSQL.