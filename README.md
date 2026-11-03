# Idempotency in Practice

**Understanding retries, duplicate requests, and safe execution in distributed backend systems — through runnable Go and PostgreSQL examples.**

A hands-on engineering repository exploring how retries can create duplicate side effects, why traditional concurrency safety isn't enough, and how to build reliable, idempotent backend operations.

Rather than explaining concepts through theory alone, this project reproduces real failure scenarios, implements different solutions, and verifies their behavior using integration tests.

## Why This Repository?

Distributed systems are built around the assumption that failures will happen.

A client times out. A database transaction commits, but its response never reaches the caller. A message is redelivered. Multiple requests arrive simultaneously.

In each situation, a caller may execute the same logical operation more than once.

Consider a payment API:

```text
Client sends payment request
         |
         v
Server processes payment
         |
         v
Database transaction commits
         |
         X  HTTP response lost
         |
         v
Client retries
         |
         v
Another payment is created
```

**The core problem:**

> The request timed out. Did the operation succeed or fail?

Without idempotency, retrying an operation can introduce duplicate payments, inconsistent inventory, repeated order creation, and other unintended business effects.

This repository explores how to make those retries safe.

## What You'll Learn

- Idempotency fundamentals and HTTP semantics
- Why retries create duplicate side effects
- Idempotency keys and request fingerprinting
- PostgreSQL transactions and unique constraints
- Concurrent request handling and lock contention
- Transaction rollback and failure recovery
- Ambiguous commit outcomes and response replay
- Database-level versus request-level idempotency
- Testing concurrency and failure scenarios
- Trade-offs involved in building reliable distributed systems

## Hands-On Examples

Each example is independently runnable and includes its own documentation, implementation, and tests.

| Example | Concept | Implementation |
|---|---|---|
| [01 — Naive Duplicate Payment](examples/01-naive-duplicate-charge/README.md) | How duplicate requests create duplicate records | Go + PostgreSQL |
| [02 — HTTP Idempotency Keys](examples/02-idempotency-key-http/README.md) | Safe retries using keys, transactions, and stored responses | Go + PostgreSQL |
| 03 — Database Constraints & Upsert | Enforcing business uniqueness through PostgreSQL | Planned |
| 04 — Kafka Consumer Deduplication | Handling duplicate event delivery using the inbox pattern | Planned |

### Demo 01 — The Problem

An intentionally non-idempotent payment API.

The same logical operation is executed through 10 concurrent HTTP requests.

```text
10 concurrent requests
         |
         v
     Payment API
         |
         v
     PostgreSQL
         |
         v
   10 payment records
```

**What it demonstrates:** A valid database transaction, UUID primary key, and concurrency-safe persistence mechanism do not automatically provide business-level idempotency.

[Explore Demo 01](examples/01-naive-duplicate-charge/README.md)

### Demo 02 — The Solution

Introduces an `Idempotency-Key` header, request fingerprinting, atomic PostgreSQL transactions, and stored response replay.

```text
10 concurrent requests
     Same key
         |
         v
     Payment API
         |
         v
  Idempotency check
         |
         v
     PostgreSQL
         |
         v
    1 payment record
         |
         v
  1 original response
  9 replayed responses
```

**What it demonstrates:** Multiple attempts can safely represent one logical business operation when execution and its idempotency record are committed atomically.

[Explore Demo 02](examples/02-idempotency-key-http/README.md)

## Architecture

Both examples use a layered backend architecture.

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

| Component | Responsibility |
|---|---|
| HTTP Handler | Request parsing, header validation, and HTTP responses |
| Service | Business rules, input validation, and orchestration |
| Repository | Database operations and transaction handling |
| PostgreSQL | Durable persistence, constraints, and concurrency coordination |

Demo 02 extends this architecture with database-backed idempotency coordination.

The payment insert and idempotency response are persisted within the same transaction.

### Design Principles

- Separation of concerns
- Dependency injection through Go interfaces
- Explicit transaction boundaries
- Context propagation
- Database-enforced concurrency guarantees
- Reproducible integration tests
- Minimal abstractions around actual business operations

## Tech Stack

| Category | Technology |
|---|---|
| Language | Go |
| Database | PostgreSQL |
| Database Driver | pgx/v5 |
| Containerization | Docker, Docker Compose |
| HTTP | Go `net/http` |
| Testing | Go testing, `httptest` |
| Database Migrations | golang-migrate |
| Configuration | Environment variables, godotenv |

Kafka and other distributed infrastructure will be introduced in later examples where they add meaningful learning value.

## Repository Structure

```text
idempotency-in-practice/
├── README.md
├── LICENSE
├── CONTRIBUTING.md
├── .gitignore
├── .env.example
├── docker-compose.yml
├── go.mod
├── go.sum
│
├── docs/
│   ├── 01-what-and-why.md
│   ├── 02-what-breaks.md
│   ├── 03-patterns.md
│   ├── 04-pitfalls-and-tradeoffs.md
│   └── 05-checklist.md
│
├── examples/
│   ├── 01-naive-duplicate-charge/
│   │   ├── main.go
│   │   ├── handler.go
│   │   ├── service.go
│   │   ├── repository.go
│   │   ├── model.go
│   │   ├── payment_test.go
│   │   └── README.md
│   │
│   └── 02-idempotency-key-http/
│       ├── main.go
│       ├── handler.go
│       ├── service.go
│       ├── repository.go
│       ├── model.go
│       ├── idempotency_test.go
│       ├── lost_response_test.go
│       ├── edge_cases_test.go
│       ├── lock_contention_test.go
│       └── README.md
│
└── migrations/
    ├── 000001_create_payments.up.sql
    ├── 000001_create_payments.down.sql
    ├── 000002_create_idempotency_keys.up.sql
    └── 000002_create_idempotency_keys.down.sql
```

The structure above represents the intended layout. Some documentation and supporting files may still be under development.

## Getting Started

### Prerequisites

Ensure the following are installed:

- Go
- Git
- Docker Desktop with Docker Compose
- golang-migrate CLI, if using versioned database migrations

### 1. Clone the Repository

```bash
git clone https://github.com/dcodes-0x/idempotency-in-practice.git

cd idempotency-in-practice
```

### 2. Configure the Environment

Copy `.env.example` to `.env`.

On Windows PowerShell:

```powershell
Copy-Item .env.example .env
```

Example configuration:

```dotenv
APP_ENV=development
APP_PORT=8080
IDEMPOTENT_APP_PORT=8081

DATABASE_URL=postgres://dev:devpassword@localhost:55432/idempotency?sslmode=disable
```

The host port `55432` corresponds to the author's local Docker setup. Set it to the port configured in your own `docker-compose.yml`.

Never commit `.env` files containing credentials.

### 3. Start PostgreSQL

```bash
docker compose up -d
```

Check container status:

```bash
docker compose ps
```

PostgreSQL should be healthy before starting the applications.

### 4. Apply Database Migrations

For a fresh database, apply the versioned migrations using `golang-migrate`.

Windows PowerShell:

```powershell
$env:DATABASE_URL = "postgres://dev:devpassword@localhost:55432/idempotency?sslmode=disable"

migrate -path ./migrations -database $env:DATABASE_URL up
```

Check the migration version:

```powershell
migrate -path ./migrations -database $env:DATABASE_URL version
```

The expected schema version after applying both initial migrations is `2`.

If your database tables were created manually before introducing migration tracking, verify the schema and baseline the appropriate version rather than executing duplicate table-creation scripts.

### 5. Run Demo 01

```bash
go run ./examples/01-naive-duplicate-charge
```

Server:

```text
http://localhost:8080
```

### 6. Run Demo 02

Open another terminal:

```bash
go run ./examples/02-idempotency-key-http
```

Server:

```text
http://localhost:8081
```

Both examples operate on the same local PostgreSQL database, using separate HTTP ports.

## API Examples

### Demo 01 — Non-Idempotent Payment

```bash
curl --location 'http://localhost:8080/payments' \
--header 'Content-Type: application/json' \
--data '{
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000
}'
```

Send the same request multiple times.

Each successful request creates a new payment record.

### Demo 02 — Idempotent Payment

```bash
curl --location 'http://localhost:8081/payments' \
--header 'Content-Type: application/json' \
--header 'Idempotency-Key: payment-order-1001' \
--data '{
    "order_id": "550e8400-e29b-41d4-a716-446655440000",
    "amount": 1000
}'
```

The first successful request creates a payment.

Subsequent requests with the same key and equivalent payload return the stored result without creating another payment.

A replayed response includes:

```http
Idempotency-Replayed: true
```

Reusing the same key with a different payload returns HTTP `422 Unprocessable Entity`.

## Testing

The repository uses integration tests against a real PostgreSQL instance rather than relying exclusively on mocked database behavior.

### Run Demo 01 Tests

```bash
go test -v ./examples/01-naive-duplicate-charge -count=1
```

### Run Demo 02 Tests

```bash
go test -v ./examples/02-idempotency-key-http -count=1
```

### Run All Tests

```bash
go test -v ./... -count=1
```

The integration tests require the PostgreSQL container to be running.

### Test Coverage

| Test | Scenario |
|---|---|
| Concurrent duplicate payments | Reproduces 10 payment records without idempotency |
| Concurrent idempotent payments | Verifies one payment from 10 concurrent requests |
| Lost response after commit | Ensures retries retrieve the committed result |
| Same key, different payload | Rejects conflicting requests |
| Transaction rollback | Ensures uncommitted key claims do not persist |
| Lock contention | Tests bounded waiting and retry after conflict resolution |

Tests intentionally use isolated identifiers to minimize interference between runs.

## Key Engineering Concepts

### Idempotency Is Not the Same as Concurrency Safety

A database can execute 10 concurrent inserts correctly and still create 10 unintended records.

Idempotency is about the resulting business effect, not merely avoiding data races.

### UUIDs Do Not Prevent Logical Duplicates

Unique primary keys identify database rows.

They do not necessarily identify a unique business operation.

### Transaction Boundaries Are Critical

For the single-database idempotency example, payment creation and idempotency-result persistence must commit atomically.

Otherwise, a payment could succeed while its deduplication record is lost.

### Unique Constraints Enable Atomic Coordination

PostgreSQL's uniqueness enforcement prevents competing transactions from successfully claiming the same idempotency key.

This avoids unsafe read-before-write assumptions.

### Request Fingerprints Protect Operation Identity

An idempotency key must not silently identify different business requests.

A stored request fingerprint enables detection of conflicting payloads.

### Exactly-Once Execution Is Not Guaranteed

Idempotency does not eliminate duplicate delivery or repeated execution attempts.

It provides a mechanism for repeated attempts to produce the intended business effect once, within a defined scope.

## Limitations

This repository is educational and intentionally focuses on isolated engineering concepts.

The initial implementations do not yet address every concern required by a production payment platform.

Areas for further exploration include:

- Tenant-scoped idempotency keys
- Key expiration and cleanup
- Recovery from permanently committed in-progress operations
- External payment gateway idempotency
- Cross-service consistency
- Backward-compatible API and schema evolution
- Retry storms, backoff, and circuit breakers
- Observability and operational metrics

All payments in the examples are simulated database operations. No actual payment gateway or financial transaction is involved.

## Learning Roadmap

| Phase | Topic | Status |
|---|---|---|
| 01 | Naive duplicate payment | Implemented |
| 02 | HTTP idempotency keys | Implemented; extended testing |
| 03 | Database constraints and upsert | Planned |
| 04 | Kafka consumer deduplication | Planned |
| 05 | Idempotency across microservices | Future exploration |

## References

- [HTTP Semantics — RFC 9110](https://www.rfc-editor.org/rfc/rfc9110.html)
- [PostgreSQL Transactions](https://www.postgresql.org/docs/current/tutorial-transactions.html)
- [PostgreSQL INSERT and ON CONFLICT](https://www.postgresql.org/docs/current/sql-insert.html)
- [PostgreSQL Explicit Locking](https://www.postgresql.org/docs/current/explicit-locking.html)
- [golang-migrate](https://github.com/golang-migrate/migrate)

## Contributing

Contributions, bug reports, suggestions, and additional failure scenarios are welcome.

Please keep examples focused, reproducible, and supported by appropriate tests.

## License

This project is licensed under the [MIT License](LICENSE).

---

**Built by [Debmalya Chatterjee](https://github.com/dcodes-0x)**

Software Engineer II | Backend Engineering & Distributed Systems

[GitHub](https://github.com/dcodes-0x) · [LinkedIn](https://www.linkedin.com/in/debmalya-chatterjee-dc/)