// Package home owns Carbon's machine-local home manifest.
//
// A home is rooted at a caller-selected main directory. Its only durable metadata is
// <main>/.carbon/home.json; every cluster receives its own data root below .carbon,
// while projects merely describe software surfaces that share that cluster task store.
// The package intentionally does not read or rewrite legacy .cairn task files.
package home
