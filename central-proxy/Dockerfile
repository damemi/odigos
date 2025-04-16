# Use the correct Go version
FROM golang:1.23-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

COPY central-proxy/ central-proxy

WORKDIR /app/central-proxy
# Copy and download dependencies 
RUN go mod download
# Build the binary
RUN go build -o central-proxy main.go

# Use a minimal runtime image
FROM alpine:latest

# Set working directory
WORKDIR /root/

# Copy the built binary from the builder stage
COPY --from=builder /app/central-proxy .

# Expose the necessary port
EXPOSE 8082
USER 65532:65532

# Run the application
CMD ["./central-proxy"]
