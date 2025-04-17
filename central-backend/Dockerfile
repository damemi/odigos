# 1️⃣ Build Stage: Compile Go Binary
FROM golang:1.23-alpine AS builder

# Set environment variables for Go module caching
ENV GO111MODULE=on \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Set working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum to download dependencies
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go application
RUN go build -o central-backend .

# 2️⃣ Runtime Stage: Minimal Image for Execution
FROM registry.fedoraproject.org/fedora-minimal:38                                                                                                                                                                                                                                                                                                                                                        

# Set working directory inside the container
WORKDIR /app

# Copy the compiled Go binary from the build stage
COPY --from=builder /app/central-backend .

# Expose the backend port
EXPOSE 8081

# Run the backend service
CMD ["/app/central-backend"]
