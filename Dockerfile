# ---- build stage ----
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /fare-svc ./cmd/server

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /fare-svc /fare-svc

EXPOSE 3005

ENTRYPOINT ["/fare-svc"]
