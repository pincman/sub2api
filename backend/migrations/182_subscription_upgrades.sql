-- Durable metadata for quota-value based subscription upgrades.
-- The source subscription is suspended while the payment order is pending,
-- then either restored on cancellation/expiry or atomically replaced on pay.
ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS upgrade_source_subscription_id BIGINT,
    ADD COLUMN IF NOT EXISTS upgrade_snapshot JSONB;

CREATE INDEX IF NOT EXISTS idx_payment_orders_upgrade_source_subscription_id
    ON payment_orders (upgrade_source_subscription_id)
    WHERE upgrade_source_subscription_id IS NOT NULL;

COMMENT ON COLUMN payment_orders.upgrade_source_subscription_id IS
    'Source user_subscriptions.id for a subscription_upgrade order';
COMMENT ON COLUMN payment_orders.upgrade_snapshot IS
    'Immutable server-calculated source quota, usage, residual credit and target plan snapshot';
