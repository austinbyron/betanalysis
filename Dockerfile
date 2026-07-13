FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/daemon ./cmd/daemon && \
    CGO_ENABLED=0 go build -o /out/betanalysis ./cmd/cli

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/daemon /usr/local/bin/daemon
COPY --from=builder /out/betanalysis /usr/local/bin/betanalysis

CMD ["daemon"]
