package core

import "crypto/rand"

func readRandom(b []byte) (int, error) { return rand.Read(b) }
