-- =============================================================================
-- GeoScale: PostgreSQL Schema for 6DoF Spatial Events
-- =============================================================================
-- This script runs automatically when the PostgreSQL container starts for the
-- first time (mounted as /docker-entrypoint-initdb.d/init.sql).
--
-- Table: spatial_events
--   Stores one row per 6DoF pose update from an autonomous vehicle.
--   Position  = (latitude, longitude, altitude_meters)  → 3 Degrees of Freedom
--   Orientation = (qx, qy, qz, qw) quaternion           → 3 Degrees of Freedom
-- =============================================================================

CREATE TABLE IF NOT EXISTS spatial_events (
    -- Primary key: auto-incrementing 64-bit integer.
    -- BIGSERIAL instead of SERIAL because at 2,000 events/sec we'd exhaust
    -- a 32-bit int (~2.1 billion) in about 12 days. BIGSERIAL is ~9.2 quintillion.
    id              BIGSERIAL        PRIMARY KEY,

    -- Which vehicle generated this event (e.g., "av-001").
    vehicle_id      VARCHAR(64)      NOT NULL,

    -- When the vehicle GENERATED this pose (set by the producer).
    -- TIMESTAMPTZ = timestamp with time zone — critical for distributed
    -- systems where producer and consumer may be in different time zones.
    event_timestamp TIMESTAMPTZ      NOT NULL,

    -- Position: 3 Degrees of Freedom
    latitude        DOUBLE PRECISION NOT NULL,
    longitude       DOUBLE PRECISION NOT NULL,
    altitude_meters DOUBLE PRECISION NOT NULL,

    -- Orientation: Quaternion (4 components encoding 3 rotational DoF).
    -- A valid quaternion satisfies: qx² + qy² + qz² + qw² = 1.0
    -- We store all 4 components; the consumer validates before inserting.
    qx              DOUBLE PRECISION NOT NULL,
    qy              DOUBLE PRECISION NOT NULL,
    qz              DOUBLE PRECISION NOT NULL,
    qw              DOUBLE PRECISION NOT NULL,

    -- When OUR SYSTEM wrote this row (auto-filled by Postgres).
    -- Pipeline latency = ingested_at - event_timestamp.
    ingested_at     TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

-- Index for fast lookups: "give me all events for vehicle X, ordered by time."
-- This is a composite B-tree index — Postgres can use it for:
--   WHERE vehicle_id = 'av-001' ORDER BY event_timestamp
--   WHERE vehicle_id = 'av-001' AND event_timestamp > '2026-03-07'
CREATE INDEX idx_spatial_events_vehicle
    ON spatial_events (vehicle_id, event_timestamp);
