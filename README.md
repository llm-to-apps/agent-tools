# agent-tools

Lightweight tools runtime for llm-to-apps user instances.

`agent-tools` runs inside an application container and exposes a small JSON API
for coding agents:

- `GET /health`
- `GET /files/tree?path=.&maxDepth=3`
- `GET /files/read?path=README.md`
- `POST /files/write`
- `POST /files/patch`
- `POST /shell/run`
- `GET /git/status`
- `POST /git/commit`
- `POST /app/start`
- `POST /app/restart`
- `GET /app/logs?tail=200`

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
- `APP_COMMAND="npm run start"`
- `AGENT_APP_LOG=/tmp/agent-tools-app.log`

## Example

```sh
curl -s http://localhost:7070/health

curl -s http://localhost:7070/shell/run \
  -H 'Content-Type: application/json' \
  -d '{"command":"git status --short","timeoutSeconds":30}'
```
