# Security policy

## Supported version

Only the latest tagged release is supported.

## Intended deployment

Use the OpenAI Secure MCP Tunnel and leave container port 8080 unmapped. The local HTTP MCP endpoint intentionally has no OAuth implementation because it is a diagnostic endpoint inside the Home Assistant trust boundary; ChatGPT reaches the in-memory MCP transport through the authenticated tunnel instead.

Never expose ports 8080 or 8086 directly to the public internet. If you replace Secure MCP Tunnel with a public reverse proxy, implement MCP-compatible OAuth 2.1 at that boundary before using private data.

Use a dedicated InfluxDB account with `READ` permission only. The application generates InfluxQL and does not expose a raw query tool, but database permissions remain the final enforcement boundary.

## Reporting a vulnerability

Open a private GitHub security advisory in the repository rather than filing a public issue. Include reproduction steps, affected version, impact, and any suggested mitigation.
