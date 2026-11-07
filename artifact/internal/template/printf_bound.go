package template

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// fixedArgBound bounds the printed size of a number, bool or other scalar
// (a %f of 1e308 prints 309 digits).
const fixedArgBound = 512

// numericMargin is the largest formatting of one number besides its precision:
// %f of 1e308 is 309 digits, the sign and the default ".000000" (317). Checked
// for the other verbs: %e, %g, %v, %x and %b of a float64 and %d, %b, %o, %x,
// %c, %U and %q of an integer are all far below it (at most 65 characters
// without precision); an error form such as %!d(float64=1.79e+308) is under 40.
// A precision adds to it (see below).
const numericMargin = 330

// MaxFormatWidth is the hard cap of a printf width or precision.
const MaxFormatWidth = 1000

const expansionFactor = 10

// printfBound returns a cautious upper bound of the size of fmt.Sprintf(format,
// args...), computed by walking the format WITHOUT running it (fmt builds the
// whole result in memory before returning, so the size must be known first).
// Per verb it adds max(width, precision, argument size), where `*` widths and
// precisions and explicit argument indexes ([n]) are followed; a string or
// []byte argument counts its length (x doubles it, q multiplies it by 6), a
// map, slice, array or struct counts the length of its JSON encoding, any other
// argument counts fixedArgBound. Unreferenced arguments (the %!(EXTRA ...) tail)
// and the literal format are added. The bound may refuse a legitimate but
// improbable call; it never lets a huge allocation through. It stops as soon as
// the bound passes limit and then returns limit+1.
func printfBound(format string, args []any, limit int) (int, error) {
	total := len(format)
	over := func() bool { return total > limit }
	memo := map[int]int{}
	nodesMemo := map[int]int{} // leaves + containers of a collection argument (0 for a scalar)
	numsMemo := map[int]int{}  // numeric leaves of a collection argument
	argSize := func(i int) int {
		if v, ok := memo[i]; ok {
			return v
		}
		size := valueSize(args[i], limit)
		if nodes, nums := collectionStats(args[i], limit); nodes > 0 {
			nodesMemo[i], numsMemo[i] = nodes, nums
		}
		memo[i] = size
		return size
	}
	intArg := func(i int) int {
		if i < 0 || i >= len(args) {
			return 0
		}
		switch v := args[i].(type) {
		case int:
			return abs(int64(v), limit)
		case int8:
			return abs(int64(v), limit)
		case int16:
			return abs(int64(v), limit)
		case int32:
			return abs(int64(v), limit)
		case int64:
			return abs(v, limit)
		case uint:
			return abs(int64(min(uint64(v), uint64(limit)+1)), limit)
		case uint8, uint16, uint32:
			return abs(int64(reflect.ValueOf(v).Uint()), limit)
		case uint64:
			return abs(int64(min(v, uint64(limit)+1)), limit)
		case float64: // JSON numbers: fmt refuses them as widths, but stay cautious
			if v > float64(limit) || v < -float64(limit) {
				return limit + 1
			}
			return abs(int64(v), limit)
		}
		return 0
	}
	used := make([]bool, len(args))
	n := len(format)
	argNum := 0
	i := 0
	index := func() {
		if i < n && format[i] == '[' {
			if j := strings.IndexByte(format[i:], ']'); j > 0 {
				if k, err := strconv.Atoi(format[i+1 : i+j]); err == nil && k >= 1 {
					argNum = k - 1
				}
				i += j + 1
			}
		}
	}
	number := func() int {
		start := i
		for i < n && format[i] >= '0' && format[i] <= '9' {
			i++
		}
		if start == i {
			return 0
		}
		v, err := strconv.Atoi(format[start:i])
		if err != nil || v > limit {
			return limit + 1
		}
		return v
	}
	for i < n && !over() {
		if format[i] != '%' {
			i++
			continue
		}
		i++
		escaping := false // the flags # and + can add escapes
		for i < n && strings.IndexByte("+-# 0", format[i]) >= 0 {
			if format[i] == '#' || format[i] == '+' {
				escaping = true
			}
			i++
		}
		index()
		width, prec := 0, 0
		if i < n && format[i] == '*' {
			i++
			width = intArg(argNum)
			if argNum >= 0 && argNum < len(args) {
				used[argNum] = true
			}
			argNum++
		} else {
			width = number()
		}
		if i < n && format[i] == '.' {
			i++
			index()
			if i < n && format[i] == '*' {
				i++
				prec = intArg(argNum)
				if argNum >= 0 && argNum < len(args) {
					used[argNum] = true
				}
				argNum++
			} else {
				prec = number()
			}
		}
		// Hard cap: a width or precision above MaxFormatWidth, literal or taken from
		// a `*`, is refused before any computation. A document layout never needs more.
		if width > MaxFormatWidth || prec > MaxFormatWidth {
			return 0, errf(CodeLimitsExceeded, "printf width or precision above %d is refused", MaxFormatWidth)
		}
		index()
		if i >= n {
			total += 32 // %!(NOVERB)
			break
		}
		verb := format[i]
		i++
		if verb == '%' {
			total++
			continue
		}
		// %p prints a memory address: not deterministic, and it leaks memory layout.
		if verb == 'p' {
			return 0, errf(CodeForbiddenFunction, "printf verb %%p is forbidden: it prints a memory address")
		}
		if argNum >= 0 && argNum < len(args) && unsafeKind(args[argNum]) {
			return 0, errf(CodeForbiddenFunction, "printf of a pointer, function, channel or unsafe value is forbidden")
		}
		size := 32 // %!v(MISSING)
		if argNum >= 0 && argNum < len(args) {
			used[argNum] = true
			size = argSize(argNum)
			if isNumber(args[argNum]) {
				size = numericMargin + prec // a precision adds digits to the largest formatting
			}
			if nodes := nodesMemo[argNum]; nodes > 0 {
				size += numsMemo[argNum] * numericMargin
			}
			// Expansion factor for any argument, collections included: %q, %x, %X, %U and
			// the flags # and + can turn each byte into an escape (at most \xNN, 4 characters,
			// or 3 per byte for \uNNNN of a 2 byte rune, 2.5 for \UNNNNNNNN of 4 bytes, and
			// 2 per byte for %x), plus quotes and separators: 10 covers the worst case.
			if verb == 'q' || verb == 'x' || verb == 'X' || verb == 'U' || escaping {
				size *= expansionFactor
			}
		}
		if nodes := nodesMemo[argNum]; nodes > 0 && argNum < len(args) {
			// the width and precision apply to every leaf, every container and every map key
			size += max(width, prec)*nodes + nodes
		}
		argNum++
		total += max(width, prec, size)
	}
	for k := range args {
		if over() {
			break
		}
		if !used[k] {
			total += argSize(k) + 16
		}
	}
	if over() {
		return limit + 1, nil
	}
	return total, nil
}

func isBytes(v any) bool { _, ok := v.([]byte); return ok }

func abs(v int64, limit int) int {
	if v < 0 {
		v = -v
	}
	if v > int64(limit) {
		return limit + 1
	}
	return int(v)
}

// countNodes counts the leaves and containers of a value, recursively, and stops
// as soon as the count passes limit (then it returns limit+1).
func countNodes(v reflect.Value, limit int) int {
	n, _ := countStats(v, limit)
	return n
}

// countStats counts the nodes (leaves and containers) and the numeric leaves of
// a value in one bounded walk.
func countStats(v reflect.Value, limit int) (nodes, nums int) {
	n := 0
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		if n > limit {
			return
		}
		n++
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			nums++
		case reflect.Interface, reflect.Pointer:
			if !v.IsNil() {
				n-- // the pointer itself prints as its target
				walk(v.Elem())
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len() && n <= limit; i++ {
				walk(v.Index(i))
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() && n <= limit {
				walk(iter.Key()) // keys are leaves: the width applies to them too
				walk(iter.Value())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField() && n <= limit; i++ {
				walk(v.Field(i))
			}
		}
	}
	walk(v)
	if n > limit {
		return limit + 1, nums
	}
	return n, nums
}

// valueSize bounds the printed size of one argument: a string or []byte its
// length, a map, slice, array or struct the length of its JSON encoding, nil 5,
// anything else fixedArgBound.
func valueSize(a any, limit int) int {
	switch v := a.(type) {
	case string:
		return len(v)
	case []byte:
		return len(v)
	case nil:
		return 5
	}
	switch reflect.ValueOf(a).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		if b, err := json.Marshal(a); err == nil {
			return len(b)
		}
		return limit + 1
	}
	return fixedArgBound
}

func collectionStats(a any, limit int) (nodes, nums int) {
	switch reflect.ValueOf(a).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		return countStats(reflect.ValueOf(a), limit)
	}
	return 0, 0
}

// argsBound bounds fmt.Sprint-like output and escapers: the sum of the sizes
// of the arguments (collections by their JSON length), each scaled by factor
// (escapers expand), plus one separator per argument and extra. It stops as
// soon as the sum passes limit, so repeated big arguments are encoded at most
// a few times.
func argsBound(args []any, factor, extra, limit int) int {
	total := extra + len(args)
	for _, a := range args {
		total += valueSize(a, limit) * factor
		if total > limit {
			return limit + 1
		}
	}
	return total
}

func isNumber(a any) bool {
	switch reflect.ValueOf(a).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// unsafeKind reports values whose formatting exposes an address or is not
// deterministic: pointers, functions, channels, unsafe pointers and uintptr.
// Template data is decoded JSON (string, float64, bool, map, slice, nil), so none
// of them can normally arrive; they are refused anyway.
func unsafeKind(a any) bool {
	switch reflect.ValueOf(a).Kind() {
	case reflect.Pointer, reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Uintptr:
		return true
	}
	return false
}
