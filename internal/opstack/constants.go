package opstack

// wordLen is the width of an ABI-encoded word, in bytes. Every offset, length
// and padded value in the payloads this package decodes is a multiple of it.
const wordLen = 32

// Topic counts of the OP Stack logs this package parses. Both carry three
// indexed parameters plus the event signature.
const (
	// depositLogTopics: [signature, from, to, version].
	depositLogTopics = 4
	// messagePassedLogTopics: [signature, nonce, sender, target].
	messagePassedLogTopics = 4
)
