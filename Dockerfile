FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tikscope ./cmd/tikscope

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /data
COPY --from=build /out/tikscope /usr/local/bin/tikscope
ENV HTTP_ADDR=:3000 \
    SYSLOG_ADDR=:5354 \
    DATABASE_PATH=/data/tikscope.db
VOLUME ["/data"]
EXPOSE 3000/tcp 5354/udp
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -qO- http://127.0.0.1:3000/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/tikscope"]
