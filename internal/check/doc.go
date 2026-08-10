// Package check runs `sh -c` checks: cwd, timeout (kill), exit-code mapping, and
// output tail to .carbon/runs/ (SPEC §6). Leaf package. Implementation lands in
// build-order step 3.
package check
