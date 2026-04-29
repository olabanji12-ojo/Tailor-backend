# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the application
# We build from cmd/api/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o /tailor-backend ./cmd/api/main.go

# Final stage
FROM alpine:latest

WORKDIR /

# Copy the binary from the builder stage
COPY --from=builder /tailor-backend /tailor-backend

# Copy .env file if needed (though in production you usually use env vars)
# COPY .env .env 

EXPOSE 8080

CMD ["/tailor-backend"]
