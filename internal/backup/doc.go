// Package backup implements Carbon's immutable, content-addressed backup
// format. It deliberately has no dependency on Carbon's configuration or CLI:
// callers supply a stable source ID, an optional application version, stores,
// and (when encryption is enabled) a key provider.
//
// A snapshot is published by writing immutable file objects first and its
// immutable manifest last. Restore always verifies the complete snapshot and
// writes into a newly-created staging directory; replacing a live directory is
// intentionally left to the caller.
package backup
