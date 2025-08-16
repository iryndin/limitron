// pack_test.go
package limitron

import (
	"fmt"
	"math/rand"
	"testing"
)

const max48 = (1 << 48) - 1

func TestPackUnpack_Table(t *testing.T) {
	tests := []struct {
		u16  uint16
		u48  uint64
		name string
	}{
		{0, 0, "zeros"},
		{0, 1, "low48_one"},
		{1, 0, "high16_one"},
		{42, 123456789, "typical"},
		{0xFFFF, 0, "max_u16_min_u48"},
		{0, max48, "min_u16_max_u48"},
		{0xABCD, 0x123456789ABC, "patterned_values"},
		{0xFFFF, max48, "max_both"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packed := packUint16AndUint48(tt.u16, tt.u48)
			gotU16, gotU48 := unpackUint16Uint48(packed)

			if gotU16 != tt.u16 || gotU48 != tt.u48 {
				t.Fatalf("roundtrip mismatch: have (%d,%d), want (%d,%d)", gotU16, gotU48, tt.u16, tt.u48)
			}

			// Bit layout sanity check:
			if gotU16 != uint16(packed>>48) {
				t.Fatalf("upper bits incorrect: got %d", gotU16)
			}
			if gotU48 != (packed & 0xFFFFFFFFFFFF) {
				t.Fatalf("lower bits incorrect: got %d", gotU48)
			}
		})
	}
}

func TestPack_PanicsOnOverflow(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when u48 >= 2^48, got none")
		}
	}()

	_ = packUint16AndUint48(1, max48+1) // should panic
}

func TestPackUnpack_Random(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 10_000; i++ {
		u16 := uint16(r.Uint32())
		u48 := uint64(r.Uint64()) & max48 // constrain to 48 bits

		p := packUint16AndUint48(u16, u48)
		uu16, uu48 := unpackUint16Uint48(p)

		if uu16 != u16 || uu48 != u48 {
			t.Fatalf("iter %d: mismatch have (%d,%d) want (%d,%d)", i, uu16, uu48, u16, u48)
		}
	}
}

func TestPack_DoesNotModifyInputs(t *testing.T) {
	// Guard against accidental in-place modification patterns.
	u16 := uint16(0xBEEF)
	u48 := uint64(0x1234_5678_9ABC)
	_ = packUint16AndUint48(u16, u48)
	if u16 != 0xBEEF || u48 != 0x1234_5678_9ABC {
		t.Fatalf("inputs should remain unchanged; got u16=%#x u48=%#x", u16, u48)
	}
}

func TestUnpack_IdempotentWithIdentityPack(t *testing.T) {
	// If we pack the unpacked values again, result must be identical.
	values := []uint64{
		0,
		1,
		uint64(0xFFFF)<<48 | 1,
		uint64(0x1234)<<48 | 0x0000FFFFFFFFFFFF,
		uint64(0xABCD)<<48 | 0x0000123456789ABC,
	}
	for i, v := range values {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			u16, u48 := unpackUint16Uint48(v)
			if u48 > max48 {
				t.Fatalf("unpack produced u48 out of range: %#x", u48)
			}
			if repacked := packUint16AndUint48(u16, u48); repacked != v {
				t.Fatalf("repack mismatch: have %#x want %#x", repacked, v)
			}
		})
	}
}

// Go 1.18+ fuzz test: `go test -fuzz=Fuzz -run=^$`
// Ensures roundtrip property holds for all 48-bit-constrained inputs.
func FuzzPackUnpack(f *testing.F) {
	// Seeds
	f.Add(uint16(0), uint64(0))
	f.Add(uint16(0xFFFF), uint64(max48))
	f.Add(uint16(42), uint64(123456789))
	f.Add(uint16(0xABCD), uint64(0x1234_5678_9ABC))

	f.Fuzz(func(t *testing.T, u16 uint16, u48 uint64) {
		u48 &= max48 // constrain; pack must panic otherwise
		p := packUint16AndUint48(u16, u48)
		uu16, uu48 := unpackUint16Uint48(p)
		if uu16 != u16 || uu48 != u48 {
			t.Fatalf("fuzz mismatch: have (%d,%d) want (%d,%d)", uu16, uu48, u16, u48)
		}
	})
}

// (Optional) Micro-benchmarks to keep an eye on perf in CI.
func BenchmarkPack(b *testing.B) {
	u16 := uint16(0xCAFE)
	u48 := uint64(0x0123_4567_89AB)
	for i := 0; i < b.N; i++ {
		_ = packUint16AndUint48(u16, u48)
	}
}

func BenchmarkUnpack(b *testing.B) {
	p := packUint16AndUint48(0xCAFE, 0x0123_4567_89AB)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = unpackUint16Uint48(p)
	}
}

func Example_roundtrip() {
	u16, u48 := uint16(7), uint64(99)
	p := packUint16AndUint48(u16, u48)
	uu16, uu48 := unpackUint16Uint48(p)
	fmt.Printf("%d %d\n", uu16, uu48)
	// Output: 7 99
}

func Example_edgeMax() {
	p := packUint16AndUint48(0xFFFF, max48)
	uu16, uu48 := unpackUint16Uint48(p)
	fmt.Printf("%#x %#x\n", uu16, uu48)
	// Output: 0xffff 0xffffffffffff
}
