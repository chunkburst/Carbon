// Package config loads and saves .carbon/config.yaml (prefix, states, gates; SPEC §3). It is a
// pure leaf — no clock, no randomness — so id minting lives in internal/store (mintTaskID).
package config
