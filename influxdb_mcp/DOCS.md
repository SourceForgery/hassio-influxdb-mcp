# Home Assistant InfluxDB MCP

This app gives ChatGPT read-only access to historical Home Assistant data stored in an InfluxDB 1.x-compatible database. It includes the MCP server and OpenAI Secure MCP Tunnel client in one container.

## Before starting

Create a dedicated InfluxDB user with read access only. In the InfluxDB query console, adapt the database name and run:

```sql
CREATE USER "chatgpt" WITH PASSWORD 'replace-with-a-long-random-password';
GRANT READ ON "home_assistant" TO "chatgpt";
```

Do not grant `WRITE` or `ALL PRIVILEGES`.

The usual community InfluxDB app is reachable internally as `http://a0d7b954-influxdb:8086`. If your app or database has another name, change `influx_url` or `influx_database` accordingly.

## Connect through OpenAI Secure MCP Tunnel

1. In [OpenAI Platform tunnel settings](https://platform.openai.com/settings/organization/tunnels), create a tunnel and associate it with the ChatGPT workspace that will use it.
2. Create a separate runtime API key whose principal has **Tunnels Read + Use**. Do not use an admin key.
3. In this app's Configuration tab, set:

   ```yaml
   influx_username: chatgpt
   influx_password: your-influx-password
   openai_tunnel_enabled: true
   openai_tunnel_id: tunnel_...
   openai_tunnel_api_key: sk-...
   ```

4. Start the app. Its log should report `OpenAI Secure MCP Tunnel connected`; it logs only a redacted tunnel ID.
5. In ChatGPT developer mode, create a plugin/app, choose **Tunnel** as the connection type, select the tunnel, and scan the tools.

The tunnel makes outbound HTTPS connections. Neither port 8086 nor port 8080 needs to be exposed through your router.

## Tools

| Tool | Purpose |
| --- | --- |
| `list_measurements` | Find InfluxDB measurement names, including unit-based measurements such as `°C`. |
| `list_entities` | Find Home Assistant entity IDs and their measurements. |
| `list_fields` | Find available field names and types. |
| `get_latest` | Return the newest stored value for one entity. |
| `query_entity` | Return bounded raw or aggregated history for one entity. |
| `query_entities` | Query several entities with the same range and aggregation for comparisons. |

Allowed aggregations are `raw`, `mean`, `min`, `max`, `median`, `sum`, `count`, `spread`, `first`, and `last`. Raw queries and aggregated buckets are capped by `max_points`; all history queries require a start time and cannot exceed `max_lookback_days`.

## Local testing with MCP Inspector

The network port is disabled by default. If you temporarily map container port `8080` in the app's Network settings, use:

```text
http://HOME_ASSISTANT_IP:8080/mcp
```

Select **Streamable HTTP** in MCP Inspector. The local endpoint has no application-level authentication, so keep it on a trusted LAN, do not forward it through your router, and disable the port mapping after testing.

Diagnostic endpoints:

- `/healthz` checks the process.
- `/readyz` checks InfluxDB and, when enabled, tunnel readiness.
- `/` reports non-secret runtime status.

## Example requests

- “Plot `sensor.outdoor_temperature` for the last seven days, averaged hourly.”
- “Compare daily average bedroom and living-room humidity this month.”
- “What was the minimum outdoor temperature last winter?”
- “Find when `binary_sensor.front_door` changed state yesterday.”

## Troubleshooting

**InfluxDB says database not found:** verify `influx_database`. Home Assistant commonly uses `home_assistant`, but existing installations can differ.

**No entities appear:** raise `max_schema_series` if the database has more than 10,000 series. Home Assistant stores sensors with units under the unit measurement and uses `domain` and `entity_id` tags; entities without a unit may instead use their full entity ID as the measurement.

**The tunnel never becomes ready:** verify that the tunnel is associated with the correct ChatGPT workspace and Platform organization, and that the runtime-key principal has Tunnels Read + Use.

**TLS fails:** install a trusted certificate for the InfluxDB endpoint. `influx_verify_tls: false` is available for a private self-signed endpoint, but certificate verification is safer.
