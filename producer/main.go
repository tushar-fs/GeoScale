package main

// GeoScale Producer - simulates a fleet of autonomous vehicles sending
// 6DoF (position + orientation) telemetry data to Kafka.
//
// Each vehicle runs in its own goroutine, generating random poses in the
// San Francisco area and pushing them to the "spatial-events" topic.
// I used goroutines because they're lightweight (~4KB each) and let each
// vehicle operate independently — just like real AVs on the road.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"
)

// SpatialEvent is the 6DoF telemetry payload. It matches my Postgres schema.
// I used quaternions for orientation because they avoid gimbal lock.
type SpatialEvent struct {
	VehicleID string  `json:"vehicle_id"`
	Timestamp int64   `json:"timestamp"` // unix nanos, used by consumer for latency tracking
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude_meters"`
	Qx        float64 `json:"qx"`
	Qy        float64 `json:"qy"`
	Qz        float64 `json:"qz"`
	Qw        float64 `json:"qw"`
}

// getEnv lets me use the same binary locally (defaults) and in K8s (env vars).
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	kafkaBroker   = getEnv("KAFKA_BROKER", "localhost:9092")
	kafkaTopic    = "spatial-events"
	numVehicles   = 5
	sendInterval  = 500 * time.Millisecond
	flushInterval = 100 * time.Millisecond
)

// generatePose creates a random 6DoF pose around San Francisco.
// I normalize the quaternion to unit length (|q| = 1) which is
// required for it to represent a valid rotation.
func generatePose(vehicleID string) SpatialEvent {
	lat := 37.7749 + (rand.Float64()-0.5)*0.01
	lon := -122.4194 + (rand.Float64()-0.5)*0.01
	alt := 10.0 + rand.Float64()*50.0

	qx := rand.Float64()*2 - 1
	qy := rand.Float64()*2 - 1
	qz := rand.Float64()*2 - 1
	qw := rand.Float64()*2 - 1
	norm := math.Sqrt(qx*qx + qy*qy + qz*qz + qw*qw)
	qx, qy, qz, qw = qx/norm, qy/norm, qz/norm, qw/norm

	return SpatialEvent{
		VehicleID: vehicleID,
		Timestamp: time.Now().UnixNano(),
		Latitude:  lat,
		Longitude: lon,
		Altitude:  alt,
		Qx:        qx,
		Qy:        qy,
		Qz:        qz,
		Qw:        qw,
	}
}

// simulateVehicle runs one goroutine per vehicle. It generates a pose every
// sendInterval and pushes it to Kafka. The select{} loop watches for context
// cancellation so I can shut down the service cleanly on Ctrl+C.
func simulateVehicle(ctx context.Context, wg *sync.WaitGroup, writer *kafka.Writer, id string) {
	defer wg.Done()

	ticker := time.NewTicker(sendInterval)
	defer ticker.Stop()

	log.Printf("[%s] started", id)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[%s] shutting down", id)
			return
		case <-ticker.C:
			event := generatePose(id)

			data, err := json.Marshal(event)
			if err != nil {
				log.Printf("[%s] marshal error: %v", id, err)
				continue
			}

			// I use vehicle_id as the key to ensure all messages from the same
			// vehicle go to the same partition — preserving per-vehicle ordering.
			err = writer.WriteMessages(ctx, kafka.Message{
				Key:   []byte(id),
				Value: data,
			})
			if err != nil {
				log.Printf("[%s] write error: %v", id, err)
				continue
			}

			log.Printf("[%s] sent: lat=%.4f lon=%.4f alt=%.1f",
				id, event.Latitude, event.Longitude, event.Altitude)
		}
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Println("=== GeoScale Producer Starting ===")
	log.Printf("vehicles=%d interval=%v broker=%s topic=%s",
		numVehicles, sendInterval, kafkaBroker, kafkaTopic)

	// I added graceful shutdown so context cancels on SIGINT/SIGTERM.
	// This is important in K8s where pods get SIGTERM on scale-down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// I instantiated one shared writer for all goroutines. kafka.Writer is thread-safe
	// and batches writes internally, so I didn't need to create
	// one per goroutine — that would just waste TCP connections.
	writer := &kafka.Writer{
		Addr:         kafka.TCP(kafkaBroker),
		Topic:        kafkaTopic,
		Balancer:     &kafka.Hash{},
		BatchTimeout: flushInterval,
		RequiredAcks: kafka.RequireOne, // wait for leader ack
	}
	defer writer.Close()

	var wg sync.WaitGroup
	for i := 0; i < numVehicles; i++ {
		vehicleID := fmt.Sprintf("av-%03d", i)
		wg.Add(1)
		go simulateVehicle(ctx, &wg, writer, vehicleID)
	}

	log.Printf("all %d vehicles launched. Ctrl+C to stop.", numVehicles)
	wg.Wait()
	log.Println("=== GeoScale Producer Stopped ===")
}
