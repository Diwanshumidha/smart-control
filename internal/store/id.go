package store

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// NewID returns a short, time-sortable id like "post_m3k2x9_a1b2c3".
func NewID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + strconv.FormatInt(time.Now().UnixMilli(), 36) + hex.EncodeToString(b)
}
