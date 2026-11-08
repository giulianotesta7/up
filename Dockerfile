# syntax=docker/dockerfile:1

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/up ./cmd/up

FROM alpine:3
# The database, its encryption key (up.db.key) and the API token hash
# (up.db.token) live together in /data. Keep that directory in a volume and
# back it up as one unit.
RUN adduser -D -u 10001 up && mkdir /data && chown up:up /data && chmod 700 /data
COPY --from=build /out/up /usr/local/bin/up
USER up
# Ping monitors run as this user through unprivileged ICMP sockets: Docker
# allows them by default; elsewhere pass
# --sysctl net.ipv4.ping_group_range="0 2147483647" (no capability needed).
ENV UP_DB=/data/up.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["up"]
CMD ["serve"]
