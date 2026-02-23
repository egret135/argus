# Stage 1: Build
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /argus .

# Stage 2: Runtime
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /argus /usr/local/bin/argus

RUN adduser -D -h /app argus
USER argus
WORKDIR /app

VOLUME ["/app/data"]
EXPOSE 8080

ENTRYPOINT ["argus"]
CMD ["-config", "/app/config.yaml"]
