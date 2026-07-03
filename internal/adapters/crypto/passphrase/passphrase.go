// Package passphrase generates human-friendly, high-entropy share tokens in the
// style of a Bitwarden/Diceware passphrase: several words joined by dashes.
//
// The embedded word list is the EFF "large" wordlist (7776 words), which is
// published by the Electronic Frontier Foundation under CC-BY 3.0 US
// (https://www.eff.org/dice). Each word contributes log2(7776) ≈ 12.9 bits, so
// the DefaultWords passphrase carries roughly 77 bits of entropy.
//
// Words are chosen with crypto/rand, so a generated passphrase is a suitable
// shared secret for the Noise PSK derived by the noise package.
package passphrase

import (
	"crypto/rand"
	_ "embed"
	"errors"
	"math/big"
	"strings"
)

//go:embed wordlist.txt
var rawWordlist string

// words is the parsed EFF large wordlist (7776 entries).
var words = strings.Fields(rawWordlist)

// DefaultWords is the number of words in a generated passphrase. 6 words from
// the 7776-word list is roughly 77 bits of entropy.
const DefaultWords = 6

// Generate returns a dash-separated passphrase of n words chosen uniformly at
// random with crypto/rand. It errors if n < 1 or the wordlist is missing.
func Generate(n int) (string, error) {
	if n < 1 {
		return "", errors.New("passphrase: word count must be >= 1")
	}
	if len(words) == 0 {
		return "", errors.New("passphrase: empty wordlist")
	}
	max := big.NewInt(int64(len(words)))
	chosen := make([]string, n)
	for i := range chosen {
		idx, err := rand.Int(rand.Reader, max) // uniform, unbiased
		if err != nil {
			return "", err
		}
		chosen[i] = words[idx.Int64()]
	}
	return strings.Join(chosen, "-"), nil
}
