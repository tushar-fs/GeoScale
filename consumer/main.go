package main

// GeoScale Consumer - reads 6DoF spatial events from Kafka, validates them,
// measures pipeline latency, and inserts valid records into PostgreSQL.
//
// I built this as a learning exercise to understand consumer groups, connection
// pooling, and how to measure end-to-end latency in a streaming pipeline.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq" // registers postgres driver with database/sql
	"github.com/segmentio/kafka-go"
)

// SpatialEvent must match the producer's struct exactly.
// Duplicating it here instead of sharing a package — keeps each service
// independently deployable, which is the whole point of microservices.
type SpatialEvent struct {
	VehicleID string  `json:"vehicle_id"`
	Timestamp int64   `json:"timestamp"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude_meters"`
	Qx        float64 `json:"qx"`
	Qy        float64 `json:"qy"`
	Qz        float64 `json:"qz"`
	Qw        float64 `json:"qw"`
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	kafkaBroker = getEnv("KAFKA_BROKER", "localhost:9092")
	kafkaTopic  = "spatial-events"
	groupID     = "geoscale-workers"

	pgConnStr = getEnv("PG_CONN_STR",
		"host=localhost port=5432 user=geoscale password=geoscale dbname=geoscale sslmode=disable")

	maxOpenConns = 10
	maxIdleConns = 5
)

// isValidEvent checks that coordinates are within valid ranges and the
// quaternion is normalized. This is the boundary between untrusted Kafka
// data and our trusted database — better to drop bad data than corrupt the DB.
func isValidEvent(e SpatialEvent) (bool, string) {
	if e.Latitude < -90 || e.Latitude > 90 {
		return false, fmt.Sprintf("invalid latitude: %.4f", e.Latitude)
	}
	if e.Longitude < -180 || e.Longitude > 180 {
		return false, fmt.Sprintf("invalid longitude: %.4f", e.Longitude)
	}

	// quaternion must be unit length (|q| = 1), small tolerance for float imprecision
	norm := math.Sqrt(e.Qx*e.Qx + e.Qy*e.Qy + e.Qz*e.Qz + e.Qw*e.Qw)
	if math.Abs(norm-1.0) > 0.01 {
		return false, fmt.Sprintf("non-unit quaternion: norm=%.4f", norm)
	}

	return true, ""
}

// connectDB sets up a connection pool to Postgres.
// Go's database/sql handles pooling automatically — you just configure
// the max connections and it reuses them across goroutines.
func connectDB() (*sql.DB, error) {
	db, err := sql.Open("postgres", pgConnStr)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Ping actually opens a connection — sql.Open only validates the DSN
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("db.Ping: %w", err)
	}

	log.Printf("connected to postgres (pool: open=%d idle=%d)", maxOpenConns, maxIdleConns)
	return db, nil
}

// processMessages is the main loop. Reads from Kafka, validates, measures
// latency, and inserts into Postgres. Uses ReadMessage which auto-commits
// offsets — so if we crash before inserting, the message gets redelivered
// on restart (at-least-once semantics).
func processMessages(ctx context.Context, reader *kafka.Reader, db *sql.DB) {
	insertSQL := `
		INSERT INTO spatial_events 
			(vehicle_id, event_timestamp, latitude, longitude, altitude_meters, qx, qy, qz, qw)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	stmt, err := db.PrepareContext(ctx, insertSQL)
	if err != nil {
		log.Fatalf("prepare insert: %v", err)
	}
	defer stmt.Close()

	var processed, dropped int64

	log.Println("waiting for messages...")

	for {
		msg, err := reader.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("read error: %v", err)
			continue
		}

		var event SpatialEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Printf("bad json: %v", err)
			dropped++
			continue
		}

		if valid, reason := isValidEvent(event); !valid {
			log.Printf("[%s] dropped: %s", event.VehicleID, reason)
			dropped++
			continue
		}

		// Latency = time since the producer stamped the message.
		// This measures the full pipeline: producer -> kafka -> consumer.
		producedAt := time.Unix(0, event.Timestamp)
		latency := time.Since(producedAt)

		_, err = stmt.ExecContext(ctx,
			event.VehicleID,
			producedAt,
			event.Latitude,
			event.Longitude,
			event.Altitude,
			event.Qx, event.Qy, event.Qz, event.Qw,
		)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("[%s] insert error: %v", event.VehicleID, err)
			continue
		}

		processed++
		log.Printf("[%s] inserted | p=%d off=%d | latency=%v",
			event.VehicleID, msg.Partition, msg.Offset, latency)
	}

	log.Printf("done. processed=%d dropped=%d", processed, dropped)
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Println("=== GeoScale Consumer Starting ===")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := connectDB()
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer db.Close()

	// GroupID is the key — all consumers with the same group ID share
	// the partitions. With 3 replicas and 3 partitions, each pod gets
	// exactly one partition. Kafka handles the assignment automatically.
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{kafkaBroker},
		Topic:       kafkaTopic,
		GroupID:     groupID,
		MinBytes:    1,
		MaxBytes:    10e6,
		StartOffset: kafka.FirstOffset,
	})
	defer reader.Close()

	log.Printf("kafka reader: topic=%s group=%s", kafkaTopic, groupID)
	processMessages(ctx, reader, db)
	log.Println("=== GeoScale Consumer Stopped ===")
}
