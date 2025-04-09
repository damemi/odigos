# Use the correct Go version
FROM golang:1.23-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire application code
COPY . .

# Build the binary
RUN go build -o centralized-proxy main.go

# Use a minimal runtime image
FROM alpine:latest

# Set working directory
WORKDIR /root/

# Copy the built binary from the builder stage
COPY --from=builder /app/centralized-proxy .

# Expose the necessary port
EXPOSE 8082

# Run the application
CMD ["./centralized-proxy"]
