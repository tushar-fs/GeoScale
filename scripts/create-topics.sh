#!/bin/bash
# =============================================================================
# GeoScale: Kafka Topic Initialization
# =============================================================================
# This script is executed by the 'kafka-init' container AFTER the Kafka broker
# is healthy. It creates the 'spatial-events' topic with 3 partitions.
#
# Why 3 partitions?
#   - We will deploy exactly 3 consumer replicas (Phase 4).
#   - Kafka assigns at most 1 consumer per partition within a consumer group.
#   - 3 partitions + 3 consumers = perfect 1:1 mapping = max parallelism.
#   - 2 partitions + 3 consumers = 1 consumer sits idle (wasted).
#   - 4 partitions + 3 consumers = 1 consumer handles 2 partitions (uneven).
#
# Why --replication-factor 1?
#   - We only have 1 broker in local development.
#   - In production, you'd set this to 3 across a multi-broker cluster
#     for fault tolerance (any 2 brokers can die and data survives).
#
# Why --if-not-exists?
#   - Makes this script idempotent — safe to re-run without errors.
#   - If the topic already exists, this command is a no-op.
# =============================================================================

echo "Creating Kafka topic: spatial-events (3 partitions)..."

/opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:29092 \
  --create \
  --if-not-exists \
  --topic spatial-events \
  --partitions 3 \
  --replication-factor 1

echo "Topic created. Verifying..."

# Describe the topic to confirm partition count in the logs.
/opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:29092 \
  --describe \
  --topic spatial-events

echo "Kafka topic initialization complete."
