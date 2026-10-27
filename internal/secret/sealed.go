// Package secret encrypts SMTP passwords with the daemon's public key, so
// the shared database never holds a readable password. Only the process that
// can read the private key file decrypts them.
package secret

// Sealed is a value encrypted to one public key. Key records which public key
// it was sealed to, so a lost private key can be detected without decrypting.
type Sealed struct {
	Key  []byte
	Data []byte
}

// Empty reports whether nothing was sealed.
func (s Sealed) Empty() bool {
	return len(s.Data) == 0
}
