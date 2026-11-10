package template

// QR Code encoder for the `qrModules` template function (contract qr/v1,
// AGENT-1919). Minimal and standard-library only: byte mode (the UTF-8 bytes of the
// text, behind an ECI 26 segment when a byte is outside ASCII), versions 1 to qrMaxVersion, the four error correction levels, mask chosen by
// the lowest penalty (first of the lowest on ties), so the same input always gives the
// same matrix. The same file is copied in pilote-maurice
// contracts/template/v1/tools/render_reference/main.go (reference renderer): both
// must keep producing the same matrices (see TestQRModulesReferenceVectors).
//
// Structure follows ISO/IEC 18004 (finder, alignment and timing patterns, format and
// version information, Reed-Solomon over GF(256) modulo 0x11D, block interleaving,
// mask patterns 0 to 7 and the four penalty rules). Correctness is checked by decoding
// the matrices with an independent decoder (qr_decode_test.go) for every version and
// level used by the tests.

import (
	"fmt"
	"strings"
)

const (
	// qrMaxInputBytes bounds the text (UTF-8 bytes) given to qrModules.
	qrMaxInputBytes = 512
	// qrMaxVersion is the largest version produced. 512 bytes at level H need
	// version 25 (117 x 117 modules); the standard goes to 40 but nothing here needs it.
	qrMaxVersion = 25
)

// qrECL is the error correction level: index into the tables, in the order L, M, Q, H.
type qrECL int

const (
	qrL qrECL = iota
	qrM
	qrQ
	qrH
)

var qrFormatBits = [4]int{1, 0, 3, 2} // L, M, Q, H

// Error correction codewords per block, then number of blocks, indexed [level][version] (index 0 unused).
// Versions 1 to qrMaxVersion only; the tables are verified by decoding every length from 1 to 512 bytes.
var qrECCPerBlock = [4][qrMaxVersion + 1]int{
	{0, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26},
	{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28},
	{0, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30},
	{0, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30},
}
var qrNumBlocks = [4][qrMaxVersion + 1]int{
	{0, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12},
	{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21},
	{0, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29},
	{0, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35},
}

// qrParseLevel maps "L", "M", "Q", "H" (upper case only) to a level.
func qrParseLevel(s string) (qrECL, bool) {
	switch s {
	case "L":
		return qrL, true
	case "M":
		return qrM, true
	case "Q":
		return qrQ, true
	case "H":
		return qrH, true
	}
	return 0, false
}

// qrRawModules is the number of modules that carry data or error correction bits.
func qrRawModules(ver int) int {
	n := (16*ver+128)*ver + 64
	if ver >= 2 {
		na := ver/7 + 2
		n -= (25*na-10)*na - 55
		if ver >= 7 {
			n -= 36
		}
	}
	return n
}

// qrDataCodewords is the number of data codewords of a version at a level.
func qrDataCodewords(ver int, ecl qrECL) int {
	return qrRawModules(ver)/8 - qrECCPerBlock[ecl][ver]*qrNumBlocks[ecl][ver]
}

// qrSmallestVersion returns the smallest version whose byte-mode capacity holds n bytes.
func qrSmallestVersion(n int, ecl qrECL) (int, bool) {
	return qrSmallestVersionECI(n, ecl, 0)
}

// qrSmallestVersionECI is qrSmallestVersion with extraBits of header before the byte
// segment (12 for the ECI segment: mode 4 bits + designator 8 bits).
func qrSmallestVersionECI(n int, ecl qrECL, extraBits int) (int, bool) {
	for ver := 1; ver <= qrMaxVersion; ver++ {
		countBits := 8
		if ver >= 10 {
			countBits = 16
		}
		if extraBits+4+countBits+8*n <= qrDataCodewords(ver, ecl)*8 {
			return ver, true
		}
	}
	return 0, false
}

// qrEncode returns the module matrix (true = dark) of the smallest QR code that holds data.
func qrEncode(data []byte, ecl qrECL) ([][]bool, int, error) {
	// A text with a byte outside ASCII gets an ECI segment (designator 26, UTF-8) before the
	// byte segment, so that a reader that defaults to ISO-8859-1 still reads UTF-8. Pure ASCII
	// keeps no ECI (compatibility and space).
	eci := false
	for _, b := range data {
		if b >= 0x80 {
			eci = true
			break
		}
	}
	extra := 0
	if eci {
		extra = 12
	}
	ver, ok := qrSmallestVersionECI(len(data), ecl, extra)
	if !ok {
		return nil, 0, fmt.Errorf("%d bytes do not fit in version %d", len(data), qrMaxVersion)
	}
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>uint(i))&1 == 1)
		}
	}
	if eci {
		put(7, 4)  // ECI mode
		put(26, 8) // designator 26 = UTF-8 (one byte: values below 128)
	}
	put(4, 4) // byte mode
	if ver < 10 {
		put(len(data), 8)
	} else {
		put(len(data), 16)
	}
	for _, b := range data {
		put(int(b), 8)
	}
	capBits := qrDataCodewords(ver, ecl) * 8
	for i := 0; i < 4 && len(bits) < capBits; i++ {
		bits = append(bits, false)
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	cw := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b {
			cw[i/8] |= 1 << uint(7-i%8)
		}
	}
	for pad := byte(0xEC); len(cw) < qrDataCodewords(ver, ecl); pad ^= 0xEC ^ 0x11 {
		cw = append(cw, pad)
	}
	all := qrAddECCAndInterleave(cw, ver, ecl)

	size := ver*4 + 17
	g := &qrGrid{size: size, mod: make([][]bool, size), fn: make([][]bool, size)}
	for i := range g.mod {
		g.mod[i] = make([]bool, size)
		g.fn[i] = make([]bool, size)
	}
	g.drawFunctionPatterns(ver, ecl)
	g.drawCodewords(all)
	best, bestPenalty := 0, -1
	for mask := 0; mask < 8; mask++ {
		g.applyMask(mask)
		g.drawFormat(ecl, mask)
		if p := g.penalty(); bestPenalty < 0 || p < bestPenalty {
			best, bestPenalty = mask, p
		}
		g.applyMask(mask) // undo (XOR)
	}
	g.applyMask(best)
	g.drawFormat(ecl, best)
	return g.mod, ver, nil
}

func qrAddECCAndInterleave(data []byte, ver int, ecl qrECL) []byte {
	nb, eccLen := qrNumBlocks[ecl][ver], qrECCPerBlock[ecl][ver]
	raw := qrRawModules(ver) / 8
	numShort := nb - raw%nb
	shortLen := raw / nb
	div := qrRSDivisor(eccLen)
	blocks := make([][]byte, nb)
	for i, k := 0, 0; i < nb; i++ {
		n := shortLen - eccLen
		if i >= numShort {
			n++
		}
		dat := data[k : k+n]
		k += n
		// every block is shortLen+1 long; a short block keeps a hole after its data
		// (index shortLen-eccLen), which the interleaving skips
		blk := make([]byte, shortLen+1)
		copy(blk, dat)
		copy(blk[len(blk)-eccLen:], qrRSRemainder(dat, div))
		blocks[i] = blk
	}
	var out []byte
	for i := 0; i < shortLen+1; i++ {
		for j, blk := range blocks {
			if i != shortLen-eccLen || j >= numShort {
				out = append(out, blk[i])
			}
		}
	}
	return out
}

func qrRSMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x1D) // x^8 = x^4 + x^3 + x^2 + 1 (0x11D)
		z ^= ((y >> uint(i)) & 1) * x
	}
	return z
}

func qrRSDivisor(degree int) []byte {
	res := make([]byte, degree)
	res[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			res[j] = qrRSMul(res[j], root)
			if j+1 < degree {
				res[j] ^= res[j+1]
			}
		}
		root = qrRSMul(root, 2)
	}
	return res
}

func qrRSRemainder(data, div []byte) []byte {
	res := make([]byte, len(div))
	for _, b := range data {
		f := b ^ res[0]
		copy(res, res[1:])
		res[len(res)-1] = 0
		for i, c := range div {
			res[i] ^= qrRSMul(c, f)
		}
	}
	return res
}

type qrGrid struct {
	size    int
	mod, fn [][]bool // [y][x]
}

func (g *qrGrid) set(x, y int, dark bool) {
	g.mod[y][x] = dark
	g.fn[y][x] = true
}

func (g *qrGrid) drawFunctionPatterns(ver int, ecl qrECL) {
	for i := 0; i < g.size; i++ {
		g.set(6, i, i%2 == 0)
		g.set(i, 6, i%2 == 0)
	}
	finder := func(cx, cy int) {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				d := max(qrAbs(dx), qrAbs(dy))
				x, y := cx+dx, cy+dy
				if x >= 0 && x < g.size && y >= 0 && y < g.size {
					g.set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	finder(3, 3)
	finder(g.size-4, 3)
	finder(3, g.size-4)
	pos := qrAlignPositions(ver, g.size)
	n := len(pos)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if (i == 0 && j == 0) || (i == 0 && j == n-1) || (i == n-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					g.set(pos[i]+dx, pos[j]+dy, max(qrAbs(dx), qrAbs(dy)) != 1)
				}
			}
		}
	}
	g.drawFormat(ecl, 0)
	if ver >= 7 {
		rem := ver
		for i := 0; i < 12; i++ {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := ver<<12 | rem
		for i := 0; i < 18; i++ {
			dark := (bits>>uint(i))&1 == 1
			a, b := g.size-11+i%3, i/3
			g.set(a, b, dark)
			g.set(b, a, dark)
		}
	}
}

func qrAbs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func qrAlignPositions(ver, size int) []int {
	if ver == 1 {
		return nil
	}
	n := ver/7 + 2
	step := 26
	if ver != 32 {
		step = (ver*4 + n*2 + 1) / (n*2 - 2) * 2
	}
	pos := make([]int, n)
	pos[0] = 6
	for i, p := n-1, size-7; i >= 1; i, p = i-1, p-step {
		pos[i] = p
	}
	return pos
}

func (g *qrGrid) drawFormat(ecl qrECL, mask int) {
	data := qrFormatBits[ecl]<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>uint(i))&1 == 1 }
	for i := 0; i <= 5; i++ {
		g.set(8, i, bit(i))
	}
	g.set(8, 7, bit(6))
	g.set(8, 8, bit(7))
	g.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		g.set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		g.set(g.size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		g.set(8, g.size-15+i, bit(i))
	}
	g.set(8, g.size-8, true) // always dark
}

func (g *qrGrid) drawCodewords(data []byte) {
	i := 0
	for right := g.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < g.size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = g.size - 1 - vert
				}
				if !g.fn[y][x] && i < len(data)*8 {
					g.mod[y][x] = (data[i>>3]>>uint(7-i&7))&1 == 1
					i++
				}
			}
		}
	}
}

func (g *qrGrid) applyMask(mask int) {
	for y := 0; y < g.size; y++ {
		for x := 0; x < g.size; x++ {
			var inv bool
			switch mask {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (x/3+y/2)%2 == 0
			case 5:
				inv = x*y%2+x*y%3 == 0
			case 6:
				inv = (x*y%2+x*y%3)%2 == 0
			case 7:
				inv = ((x+y)%2+x*y%3)%2 == 0
			}
			if inv && !g.fn[y][x] {
				g.mod[y][x] = !g.mod[y][x]
			}
		}
	}
}

// penalty applies the four rules of the standard (runs, 2x2 blocks, finder-like
// patterns, dark proportion).
func (g *qrGrid) penalty() int {
	n := g.size
	total := 0
	at := func(line, i int, rows bool) bool {
		if rows {
			return g.mod[line][i]
		}
		return g.mod[i][line]
	}
	p1 := []bool{true, false, true, true, true, false, true, false, false, false, false}
	p2 := []bool{false, false, false, false, true, false, true, true, true, false, true}
	for _, rows := range []bool{true, false} {
		for line := 0; line < n; line++ {
			run := 1
			for i := 1; i <= n; i++ {
				if i < n && at(line, i, rows) == at(line, i-1, rows) {
					run++
					continue
				}
				if run >= 5 {
					total += 3 + run - 5
				}
				run = 1
			}
			for i := 0; i+11 <= n; i++ {
				m1, m2 := true, true
				for k := 0; k < 11 && (m1 || m2); k++ {
					v := at(line, i+k, rows)
					m1 = m1 && v == p1[k]
					m2 = m2 && v == p2[k]
				}
				if m1 {
					total += 40
				}
				if m2 {
					total += 40
				}
			}
		}
	}
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if g.mod[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n && g.mod[y][x] == g.mod[y][x+1] && g.mod[y][x] == g.mod[y+1][x] && g.mod[y][x] == g.mod[y+1][x+1] {
				total += 3
			}
		}
	}
	cells := n * n
	k := (qrAbs(dark*20-cells*10)+cells-1)/cells - 1
	return total + k*10
}

// qrMatrixText serializes the matrix: one row per line of `1` (dark) and `0`
// (light) characters, rows separated by `/`, no trailing separator.
func qrMatrixText(m [][]bool) string {
	var sb strings.Builder
	for y, row := range m {
		if y > 0 {
			sb.WriteByte('/')
		}
		for _, d := range row {
			if d {
				sb.WriteByte('1')
			} else {
				sb.WriteByte('0')
			}
		}
	}
	return sb.String()
}
