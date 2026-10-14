package chain

import (
	"encoding/json"
	"testing"
)

// FuzzDecodeLine feeds the chain line decoder arbitrary bytes: it never
// panics, and an entry it accepts encodes and decodes back to itself (what
// verify re-marshals to check a signature). Seeds: chain lines in
// testdata/fuzz/FuzzDecodeLine and the ones below.
func FuzzDecodeLine(f *testing.F) {
	f.Add([]byte(`{"seq":1,"prev":"","at":"2026-01-02T03:04:05Z","type":"round","run":"r-1","lane":1,"job":"j","round":"t","result":"passed","exit":0}`))
	f.Add([]byte(`{"seq":2,"prev":"ab","type":"repair","action":"removed 3 bytes","outputs":{"chain.tail.bak":"cd"},"signer":"ssh-ed25519 AAAA","sig":"x"}`))
	f.Add([]byte(`{"seq":"1"}`))
	f.Add([]byte(`{"seq":3,"type":"round","lane":1,"exit":null`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		e, err := decodeLine(raw)
		if err != nil {
			return
		}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("an accepted entry doesn't encode: %v", err)
		}
		e2, err := decodeLine(b)
		if err != nil {
			t.Fatalf("an encoded entry doesn't decode: %v\n%s", err, b)
		}
		if b2, _ := json.Marshal(e2); string(b2) != string(b) {
			t.Errorf("not stable through encode and decode:\n%s\n%s", b, b2)
		}
	})
}
