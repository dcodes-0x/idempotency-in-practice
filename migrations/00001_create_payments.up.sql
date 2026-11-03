CREATE TABLE IF NOT EXISTS payments (
                                        id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    status VARCHAR(30) NOT NULL DEFAULT 'SUCCESS',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    );

CREATE INDEX IF NOT EXISTS idx_payments_order_id
    ON payments(order_id);