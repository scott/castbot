# Shared runtime image. It contains castlet and a Cast-compatible runtime,
# and no agent. The agent directory is written at boot from the control plane.
#
# CASTLET_RUNTIME is the command castlet execs after it writes /app/agent.
# This image uses the protocol stand-in on 127.0.0.1:8081. A later slice can
# install Cast in this image and set CASTLET_RUNTIME to:
#   python -m cast.runtime --agent-path /app/agent --port 8081 --host 127.0.0.1

FROM golang:1.22.2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/castlet ./cmd/castlet \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/cast-standin ./cmd/cast-standin

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /app/agent \
 && chown -R nobody:nogroup /app
COPY --from=build /out/castlet /usr/local/bin/castlet
COPY --from=build /out/cast-standin /usr/local/bin/cast-standin
ENV CASTLET_RUNTIME="/usr/local/bin/cast-standin --addr 127.0.0.1:8081 --agent /app/agent"
EXPOSE 8080
USER nobody
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/castlet"]
