# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS builder
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/api .

FROM scratch AS runtime
COPY --from=builder /out/api /api
EXPOSE 8080
ENTRYPOINT ["/api"]
