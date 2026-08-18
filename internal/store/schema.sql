-- sitesync initial schema: field-deployment offline sync & reconciliation
-- All money/duration values are stored as TEXT of decimal canonical form.
CREATE TABLE IF NOT EXISTS schema_version (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS persons (
    id         TEXT PRIMARY KEY,
    code       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    role       TEXT NOT NULL,
    email      TEXT NOT NULL DEFAULT '',
    active     INTEGER NOT NULL DEFAULT 1,
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS customers (
    id         TEXT PRIMARY KEY,
    code       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    workshop   TEXT NOT NULL,
    contact    TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'active',
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
    id         TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL,
    serial     TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    model      TEXT NOT NULL DEFAULT '',
    status     TEXT NOT NULL DEFAULT 'idle',
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (customer_id) REFERENCES customers(id)
);

CREATE TABLE IF NOT EXISTS deployment_orders (
    id                   TEXT PRIMARY KEY,
    code                 TEXT NOT NULL UNIQUE,
    customer_id          TEXT NOT NULL,
    field_engineer_id    TEXT NOT NULL,
    customer_manager_id  TEXT,
    trial_id             TEXT,
    status               TEXT NOT NULL DEFAULT 'draft',
    handling_mode        TEXT NOT NULL DEFAULT 'on_site_debug',
    responsible_role     TEXT NOT NULL DEFAULT 'field_engineer',
    backfill_window_hours INTEGER NOT NULL DEFAULT 168,
    last_error           TEXT,
    retry_count          INTEGER NOT NULL DEFAULT 0,
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    FOREIGN KEY (customer_id) REFERENCES customers(id),
    FOREIGN KEY (field_engineer_id) REFERENCES persons(id),
    FOREIGN KEY (customer_manager_id) REFERENCES persons(id)
);

CREATE TABLE IF NOT EXISTS deployment_steps (
    id            TEXT PRIMARY KEY,
    order_id      TEXT NOT NULL,
    step_no       INTEGER NOT NULL,
    step_name     TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    version       INTEGER NOT NULL DEFAULT 1,
    updated_at    TEXT NOT NULL,
    UNIQUE(order_id, step_no),
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS deployment_devices (
    id        TEXT PRIMARY KEY,
    order_id  TEXT NOT NULL,
    device_id TEXT NOT NULL,
    bound_at  TEXT NOT NULL,
    version   INTEGER NOT NULL DEFAULT 1,
    UNIQUE(order_id, device_id),
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id) ON DELETE CASCADE,
    FOREIGN KEY (device_id) REFERENCES devices(id)
);

CREATE TABLE IF NOT EXISTS trial_periods (
    id                  TEXT PRIMARY KEY,
    order_id            TEXT NOT NULL UNIQUE,
    effective_from      TEXT NOT NULL,
    effective_to        TEXT NOT NULL,
    acceptance_deadline TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending',
    accepted_at         TEXT,
    accepted_by         TEXT,
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id)
);

CREATE TABLE IF NOT EXISTS inspections (
    id           TEXT PRIMARY KEY,
    order_id     TEXT NOT NULL,
    device_id    TEXT,
    round        INTEGER NOT NULL DEFAULT 1,
    type         TEXT NOT NULL DEFAULT 'first_round',
    scheduled_at TEXT NOT NULL,
    completed_at TEXT,
    status       TEXT NOT NULL DEFAULT 'dispatched',
    assignee_id  TEXT NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id),
    FOREIGN KEY (device_id) REFERENCES devices(id),
    FOREIGN KEY (assignee_id) REFERENCES persons(id)
);

CREATE TABLE IF NOT EXISTS operation_records (
    id              TEXT PRIMARY KEY,
    order_id        TEXT NOT NULL,
    device_id       TEXT NOT NULL,
    responsible_id  TEXT NOT NULL,
    occurred_at     TEXT NOT NULL,
    recorded_at     TEXT NOT NULL,
    received_at     TEXT,
    source          TEXT NOT NULL DEFAULT 'online',
    client_sequence INTEGER NOT NULL,
    hours           TEXT NOT NULL DEFAULT '0',
    content         TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending',
    change_version  INTEGER NOT NULL,
    conflict_id     TEXT,
    manual_id       TEXT,
    batch_id        TEXT,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    UNIQUE(order_id, client_sequence),
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id),
    FOREIGN KEY (device_id) REFERENCES devices(id),
    FOREIGN KEY (responsible_id) REFERENCES persons(id)
);
CREATE INDEX IF NOT EXISTS idx_op_change_version ON operation_records(change_version);
CREATE INDEX IF NOT EXISTS idx_op_order_device_occurred ON operation_records(order_id, device_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_op_status ON operation_records(status);
CREATE INDEX IF NOT EXISTS idx_op_device_date ON operation_records(device_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_op_batch ON operation_records(batch_id);

CREATE TABLE IF NOT EXISTS customer_work_hours (
    id         TEXT PRIMARY KEY,
    device_id  TEXT NOT NULL,
    work_date  TEXT NOT NULL,
    hours      TEXT NOT NULL DEFAULT '0',
    reported_by TEXT NOT NULL,
    version    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(device_id, work_date),
    FOREIGN KEY (device_id) REFERENCES devices(id),
    FOREIGN KEY (reported_by) REFERENCES persons(id)
);

CREATE TABLE IF NOT EXISTS adjudications (
    id             TEXT PRIMARY KEY,
    record_id      TEXT NOT NULL UNIQUE,
    work_hour_id   TEXT NOT NULL,
    winner          TEXT NOT NULL,
    delta_hours    TEXT NOT NULL DEFAULT '0',
    attributed_to  TEXT NOT NULL,
    adjudicator_id TEXT NOT NULL,
    reason         TEXT NOT NULL DEFAULT '',
    decided_at      TEXT NOT NULL,
    version        INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (record_id) REFERENCES operation_records(id),
    FOREIGN KEY (work_hour_id) REFERENCES customer_work_hours(id),
    FOREIGN KEY (adjudicator_id) REFERENCES persons(id)
);

CREATE TABLE IF NOT EXISTS reconciliation_bills (
    id           TEXT PRIMARY KEY,
    order_id     TEXT NOT NULL,
    period_no    INTEGER NOT NULL,
    period_start TEXT NOT NULL,
    period_end   TEXT NOT NULL,
    total_hours  TEXT NOT NULL DEFAULT '0',
    rate         TEXT NOT NULL DEFAULT '0',
    amount       TEXT NOT NULL DEFAULT '0',
    status       TEXT NOT NULL DEFAULT 'draft',
    generated_by TEXT NOT NULL,
    version      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE(order_id, period_no),
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id)
);

CREATE TABLE IF NOT EXISTS sync_batches (
    id              TEXT PRIMARY KEY,
    order_id        TEXT NOT NULL,
    lease_owner     TEXT NOT NULL DEFAULT '',
    lease_until     TEXT,
    status          TEXT NOT NULL DEFAULT 'pending',
    record_count    INTEGER NOT NULL DEFAULT 0,
    processed_count INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    retry_count     INTEGER NOT NULL DEFAULT 0,
    next_retry_at   TEXT,
    version         INTEGER NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id)
);
CREATE INDEX IF NOT EXISTS idx_sync_status ON sync_batches(status, next_retry_at);

CREATE TABLE IF NOT EXISTS manual_verifications (
    id          TEXT PRIMARY KEY,
    record_id   TEXT NOT NULL UNIQUE,
    order_id    TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending',
    reviewer_id TEXT,
    reviewed_at TEXT,
    decision    TEXT,
    note        TEXT,
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    FOREIGN KEY (record_id) REFERENCES operation_records(id),
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id)
);

CREATE TABLE IF NOT EXISTS change_counter (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    next_version INTEGER NOT NULL DEFAULT 1
);
INSERT INTO change_counter (id, next_version) VALUES (1, 1) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS sync_state (
    order_id            TEXT PRIMARY KEY,
    last_change_version INTEGER NOT NULL DEFAULT 0,
    last_pulled_at      TEXT,
    last_backfill_at    TEXT,
    updated_at          TEXT NOT NULL,
    FOREIGN KEY (order_id) REFERENCES deployment_orders(id)
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id    TEXT NOT NULL,
    actor_role  TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_logs(entity_type, entity_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs(actor_id, occurred_at);

CREATE TABLE IF NOT EXISTS permanent_failures (
    id              TEXT PRIMARY KEY,
    entity_type     TEXT NOT NULL,
    entity_id       TEXT NOT NULL,
    task_type       TEXT NOT NULL,
    last_error      TEXT NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'permanent',
    created_at      TEXT NOT NULL,
    UNIQUE(entity_type, entity_id, task_type)
);
