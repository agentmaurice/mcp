# syntax=docker/dockerfile:1.6
ARG SIDECAR_BASE_IMAGE
FROM ${SIDECAR_BASE_IMAGE}

USER root
RUN apk add --no-cache git python3 py3-pip && \
    ln -sf /usr/bin/python3 /usr/bin/python

ENV MCP_SIDECAR_RUNTIME_WORKDIR=/tmp/mcp-sidecar-workspace

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/mcp_sidecar"]
CMD ["start"]
