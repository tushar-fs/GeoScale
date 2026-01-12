# Build stage: compile with all dependencies
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o producer .

# Run stage: scratch = empty image, keeps it tiny (~10MB)
FROM scratch
COPY --from=builder /app/producer /producer
ENTRYPOINT ["/producer"]
