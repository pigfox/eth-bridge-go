package store

// withdrawalFilePerm is the mode of a persisted withdrawal file. A withdrawal
// is the proof material needed to finalize a bridge exit, so it stays readable
// only by the user that produced it.
const withdrawalFilePerm = 0o600
