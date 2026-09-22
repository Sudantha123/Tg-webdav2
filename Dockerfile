# ---------- build ----------
FROM golang:1-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags 2>/dev/null || echo docker)" -o /out/tgwebdav .

# ---------- run ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /out/tgwebdav /app/tgwebdav
ENV DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER app
ENTRYPOINT ["/app/tgwebdav"]
