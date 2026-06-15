// internal/memory/provision.go
//
// Ties the per-user DEK to the login password (no PIN; encryption on by default).
//
//   ProvisionDEK  - create a DEK, wrap it with the password, return wrapped+salt
//                   (called at signup, and lazily on first login for old accounts)
//   UnlockDEK     - given password + stored wrapped+salt, recover the DEK
//                   (called at login; the recovered DEK is pushed to Redis)
//
// The server stores only enc_dek (wrapped) + dek_salt. The password is never
// stored (bcrypt hash only), and the raw DEK lives solely in Redis during a
// session. A DB dump yields wrapped DEK + ciphertext + no password = unreadable.
package memory

// ProvisionDEK generates a fresh DEK and wraps it with a KEK derived from the
// password. Returns (wrappedDEK, salt) to persist on the user row.
func ProvisionDEK(password string) (wrapped, salt []byte, err error) {
	dek, err := NewDEK()
	if err != nil {
		return nil, nil, err
	}
	salt, err = NewSalt()
	if err != nil {
		return nil, nil, err
	}
	kek := DeriveKEK(password, salt)
	wrapped, err = WrapDEK(kek, dek)
	if err != nil {
		return nil, nil, err
	}
	return wrapped, salt, nil
}

// UnlockDEK recovers the raw DEK from the stored wrapped blob + salt using the
// password. Wrong password -> ErrDecrypt.
func UnlockDEK(password string, wrapped, salt []byte) ([]byte, error) {
	if len(wrapped) == 0 || len(salt) == 0 {
		return nil, ErrBadLength
	}
	kek := DeriveKEK(password, salt)
	return UnwrapDEK(kek, wrapped)
}
