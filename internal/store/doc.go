// Package store is the read-fresh file layer: parse (lossless), yaml.Node surgical
// writes, atomic temp+rename, dir scan, repository locking, and dangling/cycle validation
// on load (SPEC §8). Depends on task + config. Implementation lands in build-order step 5.
package store
