---
title: Antigravity
---

# Antigravity

Any Streamable HTTP MCP client can connect to Carbon's loopback server. Start it with a Home:

```sh
carbon web --home /absolute/path/to/carbon-home --compat-layer v2
```

Read the printed `CARBON_WEB_URL`, then configure the client with an explicit actor and project
session routing:

```text
<mcp-url>/mcp?actor=agent:antigravity&home=/absolute/path/to/carbon-home&routing=session
```

Keep the service on loopback. If the client runs on another machine, use an operator-controlled SSH
local forward or a VPN tunnel that terminates on loopback; do not expose Carbon directly.

Create or select one active project through MCP; it remains active until `select_project`. Use a
fixed `project` query only for pinned compatibility, and `cluster` only for an intentional shared
pool. Call `identity` after connection to validate actor, active scope, selection version, and v2.
