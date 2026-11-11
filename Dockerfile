# Shared runtime image.
# The default target (runtime) installs Cast from scott/castagent at a pinned
# commit. It does not use install.sh, which would install anthropic unpinned.
# The stand-in target is only for local tests and CI.
#
#   docker build -t castbot-runtime:s07 .
#   docker build --target standin -t castbot-runtime:s03 .
#
# CASTLET_RUNTIME was checked against cast.runtime at
# 5fc2bdb46c4041831fc51c9f909d59e54f887d31:
#   python -m cast.runtime --agent-path PATH --port PORT --host HOST

FROM golang:1.27.4 AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/castlet ./cmd/castlet \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/cast-standin ./cmd/cast-standin

FROM python:3.11-slim-bookworm AS cast
ARG CAST_COMMIT=5fc2bdb46c4041831fc51c9f909d59e54f887d31
ARG CAST_SHA256=f6df23078ffd4e4a92bb7c77c1445c7d42d1b4e60d0551a1ff2f7f90e41539e9
COPY deploy/cast-constraints.txt /tmp/cast-constraints.txt
# The base image already has CA certificates, so this stage does not run apt.
RUN python -c "import hashlib,os,urllib.request; c=os.environ['CAST_COMMIT']; urllib.request.urlretrieve('https://github.com/scott/castagent/archive/'+c+'.tar.gz','/tmp/cast.tar.gz'); h=hashlib.sha256(open('/tmp/cast.tar.gz','rb').read()).hexdigest(); assert h==os.environ['CAST_SHA256'], h" \
 && tar -xzf /tmp/cast.tar.gz -C /tmp \
 && python -m venv /opt/cast \
 && /opt/cast/bin/pip install --no-cache-dir -c /tmp/cast-constraints.txt "/tmp/castagent-${CAST_COMMIT}/python" \
 && cp /tmp/castagent-${CAST_COMMIT}/python/cast/providers/*.json /opt/cast/lib/python3.11/site-packages/cast/providers/ \
 && rm -rf /tmp/cast.tar.gz "/tmp/castagent-${CAST_COMMIT}" /root/.cache \
 && GIT_PYTHON_REFRESH=quiet /opt/cast/bin/python -c "import cast.runtime"

FROM debian:bookworm-slim AS standin
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /app/agent \
 && chown -R nobody:nogroup /app
COPY --from=gobuild /out/castlet /usr/local/bin/castlet
COPY --from=gobuild /out/cast-standin /usr/local/bin/cast-standin
ENV CASTLET_RUNTIME="/usr/local/bin/cast-standin --addr 127.0.0.1:8081 --agent /app/agent"
EXPOSE 8080
USER nobody
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/castlet"]

FROM python:3.11-slim-bookworm AS runtime
# The base image already has CA certificates, so this stage does not run apt.
RUN mkdir -p /app/agent \
 && chown -R nobody:nogroup /app
COPY --from=gobuild /out/castlet /usr/local/bin/castlet
COPY --from=cast /opt/cast /opt/cast
ENV PATH="/opt/cast/bin:${PATH}"
ENV PYTHONDONTWRITEBYTECODE=1
ENV HOME=/app
# GitPython raises on import when git is absent. Chat does not need it.
ENV GIT_PYTHON_REFRESH=quiet
ENV CASTLET_RUNTIME="/opt/cast/bin/python -m cast.runtime --agent-path /app/agent --port 8081 --host 127.0.0.1"
EXPOSE 8080
USER nobody
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/castlet"]
