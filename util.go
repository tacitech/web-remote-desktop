package main

import (
	"crypto/rand"
	"math/big"
)

// tokenAlphabet avoids look-alike characters (0/O, 1/l/I) so the code is easy
// to read off a screen and type on a phone.
const tokenAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// randToken returns a short, typeable access code. 10 chars from a 31-symbol
// alphabet ≈ 49 bits — brute-forcing that over the tunnel is impractical, and
// the code only has to be typed once per device (then it lives in a cookie).
func randToken() string {
	const n = 10
	b := make([]byte, n)
	max := big.NewInt(int64(len(tokenAlphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "changeme-set-token"
		}
		b[i] = tokenAlphabet[v.Int64()]
	}
	return string(b)
}
