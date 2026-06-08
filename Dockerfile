FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN go build -o /out/agent-tools ./cmd/agent-tools

FROM alpine:3.20
RUN apk add --no-cache bash ca-certificates git openssh-client patch
COPY --from=build /out/agent-tools /usr/local/bin/agent-tools
ENV AGENT_TOOLS_HOST=0.0.0.0 \
    AGENT_TOOLS_PORT=7070 \
    AGENT_WORKDIR=/workspace
WORKDIR /workspace
EXPOSE 7070
ENTRYPOINT ["agent-tools"]
