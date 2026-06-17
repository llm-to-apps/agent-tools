# agent-tools

Lightweight tools runtime for os7 user instances.

`agent-tools` runs inside an application container and exposes a small JSON API
for coding agents:

- `GET /health`
- `GET /files/tree?path=.&maxDepth=3`
- `GET /files/read?path=README.md&startLine=1&endLine=80`
- `POST /files/write`
- `POST /files/replace-text`
- `POST /files/patch`
- `POST /shell/run`
- `GET /git/status`
- `GET /git/diff`
- `POST /git/commit`
- `POST /app/dev/start`
- `POST /app/dev/stop`
- `POST /app/prod/restart`
- `POST /app/prod/stop`
- `POST /app/build`
- `GET /app/status`
- `GET /app/logs?process=prod&tail=200`
- `GET /runtime/status` public read-only runtime status
- `GET /runtime/events` public read-only runtime SSE stream

## Run

```sh
make run
```

Configuration:

- `AGENT_WORKDIR=/workspace`
- `AGENT_TOOLS_HOST=0.0.0.0`
- `AGENT_TOOLS_PORT=7070`
- `AGENT_TOOLS_TOKEN=` optional bearer token for tool requests
- `APP_STARTUP_COMMANDS="npm run db:deploy && npm run db:seed"` optional commands to run before the app starts
- `APP_STARTUP_TIMEOUT_SECONDS=120`
- `APP_COMMAND="npm run start"` fallback production command
- `APP_DEV_COMMAND="npm run dev:docker"` optional dev runtime command
- `APP_DEV_IDLE_TIMEOUT_SECONDS=60` stops the dev runtime after protected agent-tools inactivity; set `0` to disable
- `APP_PROD_COMMAND="npm run start"` optional production runtime command
- `APP_BUILD_COMMAND="npm run build"` optional build command used by `POST /app/build`
- `APP_BUILD_TIMEOUT_SECONDS=600`
- `APP_AUTO_RESTART=true`
- `APP_MAX_RESTARTS=5`
- `APP_RESTART_BACKOFF_SECONDS=2`
- `AGENT_APP_LOG=/tmp/agent-tools-app.log`
- `APP_DEV_LOG=/tmp/agent-tools-dev.log`

## Example

```sh
curl -s http://localhost:7070/health

curl -s http://localhost:7070/shell/run \
  -H 'Content-Type: application/json' \
  -d '{"command":"git status --short","timeoutSeconds":30}'
```
