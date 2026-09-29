package document

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	pdfreader "github.com/ledongthuc/pdf"
	"golang.org/x/text/encoding/charmap"
)

// Simple fonts address one glyph per byte, even when their ToUnicode
// codespace declaration incorrectly advertises two bytes (seen in publisher
// PDFs). The explicit bfchar/bfrange mappings remain authoritative.
func simplePDFUnicode(font pdfreader.Font) (map[byte]string, error) {
	subtype := font.V.Key("Subtype").Name()
	if subtype != "Type1" && subtype != "TrueType" && subtype != "Type3" {
		return nil, nil
	}
	cmap := font.V.Key("ToUnicode")
	if cmap.Kind() != pdfreader.Stream {
		return simplePDFDifferences(font), nil
	}
	result := map[byte]string{}
	var failure error
	put := func(source, target string) {
		if failure != nil {
			return
		}
		if len(source) == 2 && source[0] == 0 {
			source = source[1:]
		}
		if len(source) != 1 || len(target) == 0 || len(target)%2 != 0 {
			failure = fmt.Errorf("invalid simple-font ToUnicode mapping")
			return
		}
		units := make([]uint16, len(target)/2)
		for i := range units {
			units[i] = uint16(target[2*i])<<8 | uint16(target[2*i+1])
		}
		for i := 0; i < len(units); i++ {
			if units[i] >= 0xd800 && units[i] <= 0xdbff {
				if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
					failure = fmt.Errorf("invalid ToUnicode surrogate pair")
					return
				}
				i++
			} else if units[i] >= 0xdc00 && units[i] <= 0xdfff {
				failure = fmt.Errorf("invalid ToUnicode surrogate")
				return
			}
		}
		text := string(utf16.Decode(units))
		if prior, ok := result[source[0]]; ok && prior != text {
			failure = fmt.Errorf("conflicting ToUnicode mapping")
			return
		}
		result[source[0]] = text
	}
	mode, count := "", 0
	pdfreader.Interpret(cmap, func(stack *pdfreader.Stack, op string) {
		if failure != nil {
			return
		}
		switch op {
		case "findresource":
			stack.Pop()
			stack.Pop()
			stack.Push(font.V)
		case "begincmap":
			stack.Push(font.V)
		case "endcmap":
			stack.Pop()
		case "begincodespacerange":
			count = int(stack.Pop().Int64())
			if count < 0 || count > 256 {
				failure = fmt.Errorf("invalid ToUnicode codespace count")
			}
		case "endcodespacerange":
			for i := 0; i < count; i++ {
				stack.Pop()
				stack.Pop()
			}
		case "defineresource":
			stack.Pop()
			value := stack.Pop()
			stack.Pop()
			stack.Push(value)
		case "beginbfchar", "beginbfrange":
			count = int(stack.Pop().Int64())
			mode = op
			if count < 0 || count > 256 {
				failure = fmt.Errorf("ToUnicode mapping count exceeds simple-font limit")
			}
		case "endbfchar":
			if mode != "beginbfchar" || stack.Len() < count*2 {
				failure = fmt.Errorf("malformed ToUnicode bfchar")
				return
			}
			for i := 0; i < count; i++ {
				target := stack.Pop()
				source := stack.Pop()
				put(source.RawString(), target.RawString())
			}
			mode = ""
		case "endbfrange":
			if mode != "beginbfrange" || stack.Len() < count*3 {
				failure = fmt.Errorf("malformed ToUnicode bfrange")
				return
			}
			for i := 0; i < count; i++ {
				target, high, low := stack.Pop(), stack.Pop().RawString(), stack.Pop().RawString()
				if len(low) == 2 && low[0] == 0 {
					low = low[1:]
				}
				if len(high) == 2 && high[0] == 0 {
					high = high[1:]
				}
				if len(low) != 1 || len(high) != 1 || low[0] > high[0] {
					failure = fmt.Errorf("invalid simple-font ToUnicode range")
					return
				}
				length := int(high[0]) - int(low[0]) + 1
				if target.Kind() == pdfreader.Array && target.Len() != length {
					failure = fmt.Errorf("invalid ToUnicode range array")
					return
				}
				dst := []byte(target.RawString())
				for j := 0; j < length; j++ {
					if target.Kind() == pdfreader.Array {
						put(string([]byte{low[0] + byte(j)}), target.Index(j).RawString())
					} else {
						put(string([]byte{low[0] + byte(j)}), string(dst))
						for k := len(dst) - 1; k >= 0; k-- {
							dst[k]++
							if dst[k] != 0 {
								break
							}
						}
					}
				}
			}
			mode = ""
		}
	})
	if mode != "" {
		return nil, fmt.Errorf("unterminated ToUnicode mapping")
	}
	return result, failure
}

func simplePDFDifferences(font pdfreader.Font) map[byte]string {
	enc := font.V.Key("Encoding")
	if enc.Kind() != pdfreader.Dict {
		return nil
	}
	result := map[byte]string{}
	if enc.Key("BaseEncoding").Name() == "WinAnsiEncoding" {
		for i := 0; i < 256; i++ {
			result[byte(i)] = string(charmap.Windows1252.DecodeByte(byte(i)))
		}
	}
	diff := enc.Key("Differences")
	code := -1
	for i := 0; i < diff.Len(); i++ {
		v := diff.Index(i)
		if v.Kind() == pdfreader.Integer {
			code = int(v.Int64())
			continue
		}
		if v.Kind() != pdfreader.Name || code < 0 || code > 255 {
			continue
		}
		name := strings.SplitN(v.Name(), ".", 2)[0]
		parts := strings.Split(name, "_")
		var decoded strings.Builder
		known := true
		for _, part := range parts {
			if text, ok := pdfNamedGlyphs[part]; ok {
				decoded.WriteString(text)
			} else if strings.HasPrefix(part, "uni") && len(part) > 3 && (len(part)-3)%4 == 0 {
				for j := 3; j < len(part); j += 4 {
					n, e := strconv.ParseUint(part[j:j+4], 16, 16)
					if e != nil || n >= 0xd800 && n <= 0xdfff {
						known = false
						break
					}
					decoded.WriteRune(rune(n))
				}
			} else if strings.HasPrefix(part, "u") && len(part) >= 5 && len(part) <= 7 {
				n, e := strconv.ParseUint(part[1:], 16, 32)
				if e != nil || n > 0x10ffff || n >= 0xd800 && n <= 0xdfff {
					known = false
				} else {
					decoded.WriteRune(rune(n))
				}
			} else if len(part) == 1 {
				decoded.WriteString(part)
			} else {
				// The upstream Adobe glyph list handles standard names; only
				// supplement the compositional forms it does not understand.
				known = false
			}
		}
		if known {
			result[byte(code)] = decoded.String()
		} else {
			result[byte(code)] = font.Encoder().Decode(string([]byte{byte(code)}))
		}
		code++
	}
	return result
}

var pdfNamedGlyphs = map[string]string{
	"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
	"ff": "ff", "fi": "fi", "fl": "fl", "ffi": "ffi", "ffl": "ffl", "space": " ", "minus": "−",
}
