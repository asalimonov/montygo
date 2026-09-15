package mountfs

// utf8Check reproduces Rust's Utf8Error: the valid prefix length and the
// length of the invalid sequence, 0 when the input ends mid-sequence.
func utf8Check(v []byte) (validUpTo, errorLen int, ok bool) {
	n := len(v)
	cont := func(b byte) bool { return b&0xC0 == 0x80 }
	for i := 0; i < n; {
		first := v[i]
		if first < 0x80 {
			i++
			continue
		}
		width := utf8CharWidth(first)
		switch width {
		case 2:
			if i+1 >= n {
				return i, 0, false
			}
			if !cont(v[i+1]) {
				return i, 1, false
			}
		case 3:
			if i+1 >= n {
				return i, 0, false
			}
			s := v[i+1]
			valid := (first == 0xE0 && s >= 0xA0 && s <= 0xBF) ||
				(first >= 0xE1 && first <= 0xEC && cont(s)) ||
				(first == 0xED && s >= 0x80 && s <= 0x9F) ||
				(first >= 0xEE && first <= 0xEF && cont(s))
			if !valid {
				return i, 1, false
			}
			if i+2 >= n {
				return i, 0, false
			}
			if !cont(v[i+2]) {
				return i, 2, false
			}
		case 4:
			if i+1 >= n {
				return i, 0, false
			}
			s := v[i+1]
			valid := (first == 0xF0 && s >= 0x90 && s <= 0xBF) ||
				(first >= 0xF1 && first <= 0xF3 && cont(s)) ||
				(first == 0xF4 && s >= 0x80 && s <= 0x8F)
			if !valid {
				return i, 1, false
			}
			if i+2 >= n {
				return i, 0, false
			}
			if !cont(v[i+2]) {
				return i, 2, false
			}
			if i+3 >= n {
				return i, 0, false
			}
			if !cont(v[i+3]) {
				return i, 3, false
			}
		default:
			return i, 1, false
		}
		i += width
	}
	return n, 0, true
}

func utf8CharWidth(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b < 0xC2:
		return 0
	case b < 0xE0:
		return 2
	case b < 0xF0:
		return 3
	case b < 0xF5:
		return 4
	}
	return 0
}

func utf8ErrorReason(firstBad byte, errorLen int) string {
	switch {
	case errorLen == 0:
		return "unexpected end of data"
	case firstBad >= 0xC2 && firstBad <= 0xF4:
		return "invalid continuation byte"
	}
	return "invalid start byte"
}

func bytesToUTF8(b []byte) (string, *mountError) {
	start, errLen, ok := utf8Check(b)
	if ok {
		return string(b), nil
	}
	end := len(b)
	if errLen > 0 {
		end = start + errLen
	}
	reason := utf8ErrorReason(b[start], errLen)
	failure := &utf8Failure{start: start, end: end, firstByte: b[start], reason: reason}
	if len(b) <= maxUnicodeObjectLen {
		failure.object = b
		failure.hasObject = true
	}
	return "", &mountError{kind: errInvalidUTF8, utf8: failure}
}

// lossyUTF8 mirrors Rust's to_string_lossy: one U+FFFD per invalid sequence.
func lossyUTF8(s string) string {
	b := []byte(s)
	if _, _, ok := utf8Check(b); ok {
		return s
	}
	out := make([]byte, 0, len(b)+8)
	for len(b) > 0 {
		valid, errLen, ok := utf8Check(b)
		out = append(out, b[:valid]...)
		if ok {
			break
		}
		out = append(out, "�"...)
		if errLen == 0 {
			break
		}
		b = b[valid+errLen:]
	}
	return string(out)
}
