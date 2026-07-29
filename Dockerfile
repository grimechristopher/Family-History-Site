# Build
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binary: the runtime image has no libc.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/server ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/import ./cmd/import

# Run
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/import /usr/local/bin/import
USER app
EXPOSE 8080
# Templates and static files are embedded in the binary, so nothing else ships.
ENTRYPOINT ["/usr/local/bin/server"]
