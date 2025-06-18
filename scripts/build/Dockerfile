# Stage 1: Build the application and dependencies
FROM golang:1.24 AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy the Go module files
COPY go.mod go.sum ./

# Download the Go dependencies
RUN go env -w GOPROXY=https://goproxy.cn,direct && go env -w GOPRIVATE=github.com/ecodeclub
RUN go mod download && go mod tidy

# Copy the rest of the application source code
COPY singleprocess .

# Build the Go binaries
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o gateway cmd/main.go

# Stage 2: Create minimal Docker images for exec
FROM golang:1.24-alpine

# Set the working directory inside the container
WORKDIR /app

# Copy the builted gateway binary
COPY --from=builder /app/gateway .

# Copy the prod.toml for gateway
COPY --from=builder /app/config/config.docker.yaml config.yaml

# Set the executable permissions for ecodim binary
RUN chmod +x ./gateway

ENV EGO_DEBUG=true

# Set the command to run when the container starts
CMD ["./gateway", "--config=config.toml"]