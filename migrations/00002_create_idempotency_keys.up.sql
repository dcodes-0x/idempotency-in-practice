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