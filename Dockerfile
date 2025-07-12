# Build stage
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o logforwarder .

# Final stage
FROM alpine:latest
WORKDIR /app
COPY --from=builder /src/logforwarder ./
# ENTRYPOINT ["/app/logforwarder"]
