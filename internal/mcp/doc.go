// Package mcp exposes scoped agent tools as thin adapters over store + task + check.
// Frozen legacy v1 retains the historical claim verb; Carbon v2 exposes the
// version-protected lease_claim ownership primitive and selected-project durable
// event subscriptions. Event subscriptions use polling only until an MCP session
// has a verified push capability. Gate logic is not reimplemented here: verbs
// call task.Ready / task.CanTransition.
package mcp
