package controlplane

import (
	"sync"

	"golang.org/x/crypto/bcrypt"
)

var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

func dummyPasswordHash() []byte {
	dummyHashOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("mishmesh-timing-equalizer"), bcrypt.DefaultCost)
	})
	return dummyHash
}

func passwordMatches(hash, password string) bool {
	if hash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
