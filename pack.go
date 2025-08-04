package limitron

// unpackUint16Uint48 splits a 64-bit packed value into its original 16-bit and 48-bit components.
//
// Returns:
//   - The upper 16 bits (as uint16)
//   - The lower 48 bits (as uint64)
//
// This function reverses the operation performed by packUint16AndUint48.
//
// Example:
//
//	u16, u48 := unpackUint16Uint48(packed)
func unpackUint16Uint48(packed uint64) (uint16, uint64) {
	u16 := uint16(packed >> 48)
	u48 := packed & 0xFFFFFFFFFFFF
	return u16, u48
}

// packUint16AndUint48 packs a 16-bit unsigned integer (`u16`) and a 48-bit unsigned integer (`u48`)
// into a single 64-bit unsigned value.
//
// The higher 16 bits of the result store `u16`, and the lower 48 bits store `u48`.
//
// Panics if `u48` exceeds 48-bit capacity (i.e., >= 2^48).
//
// Example:
//
//	packed := packUint16AndUint48(42, 123456789)
//	// packed now holds a uint64 with 42 in upper 16 bits and 123456789 in lower 48
func packUint16AndUint48(u16 uint16, u48 uint64) uint64 {
	if u48 >= (1 << 48) {
		panic("u48 overflows 48 bits")
	}
	return (uint64(u16) << 48) | (u48 & 0xFFFFFFFFFFFF)
}
