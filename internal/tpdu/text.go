package tpdu

import (
	"errors"
	"fmt"
	"unicode/utf16"
)

type Encoding string

const (
	EncodingGSM7   Encoding = "gsm7"
	EncodingUCS2   Encoding = "ucs2"
	EncodingBinary Encoding = "binary"
)

const (
	dcsGSM7 = 0x00
	dcsUCS2 = 0x08

	gsm7Escape = 0x1b

	maxSeptets = 160
	maxParts   = 255

	concatHeaderOctets = 6
)

const (
	ieiConcat8  = 0x00
	ieiConcat16 = 0x08
)

var ErrTextTooLong = errors.New("text needs more than 255 short messages")

var gsm7Basic = []rune("@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà")

var gsm7Extension = map[byte]rune{
	0x0a: '\f', 0x14: '^', 0x28: '{', 0x29: '}', 0x2f: '\\', 0x3c: '[', 0x3d: '~', 0x3e: ']', 0x40: '|', 0x65: '€',
}

var (
	gsm7BasicIndex     = make(map[rune]byte, len(gsm7Basic))
	gsm7ExtensionIndex = make(map[rune]byte, len(gsm7Extension))
)

func init() {
	for i, r := range gsm7Basic {
		if i != gsm7Escape {
			gsm7BasicIndex[r] = byte(i)
		}
	}

	for code, r := range gsm7Extension {
		gsm7ExtensionIndex[r] = code
	}
}

type Concatenation struct {
	Reference uint16
	Part      uint8
	Total     uint8
}

type TextPart struct {
	UserDataHeader   bool
	DataCodingScheme byte
	UserDataLength   uint8
	UserData         []byte
}

func EncodeText(text string, reference uint8) ([]TextPart, Encoding, error) {
	if septets, ok := gsm7Septets(text); ok {
		parts, err := gsm7Parts(septets, reference)
		return parts, EncodingGSM7, err
	}

	parts, err := ucs2Parts(utf16.Encode([]rune(text)), reference)

	return parts, EncodingUCS2, err
}

func gsm7Septets(text string) ([][]byte, bool) {
	chars := make([][]byte, 0, len(text))

	for _, r := range text {
		if s, ok := gsm7BasicIndex[r]; ok {
			chars = append(chars, []byte{s})
			continue
		}

		if s, ok := gsm7ExtensionIndex[r]; ok {
			chars = append(chars, []byte{gsm7Escape, s})
			continue
		}

		return nil, false
	}

	return chars, true
}

func gsm7Parts(chars [][]byte, reference uint8) ([]TextPart, error) {
	total := 0
	for _, c := range chars {
		total += len(c)
	}

	if total <= maxSeptets {
		return []TextPart{gsm7Part(nil, flatten(chars))}, nil
	}

	headerSeptets := (concatHeaderOctets*8 + 6) / 7

	chunks := split(chars, maxSeptets-headerSeptets)
	if len(chunks) > maxParts {
		return nil, ErrTextTooLong
	}

	parts := make([]TextPart, len(chunks))
	for i, chunk := range chunks {
		parts[i] = gsm7Part(concatHeader(reference, uint8(len(chunks)), uint8(i+1)), chunk)
	}

	return parts, nil
}

func gsm7Part(header, septets []byte) TextPart {
	headerSeptets := (len(header)*8 + 6) / 7
	udl := headerSeptets + len(septets)

	ud := make([]byte, (udl*7+7)/8)
	copy(ud, header)

	for i, s := range septets {
		bit := (headerSeptets + i) * 7
		ud[bit/8] |= s << (bit % 8)

		if bit%8 > 1 {
			ud[bit/8+1] |= s >> (8 - bit%8)
		}
	}

	return TextPart{UserDataHeader: header != nil, DataCodingScheme: dcsGSM7, UserDataLength: uint8(udl), UserData: ud}
}

func ucs2Parts(units []uint16, reference uint8) ([]TextPart, error) {
	chars := make([][]byte, 0, len(units))

	for i := 0; i < len(units); i++ {
		n := 1
		if utf16.IsSurrogate(rune(units[i])) && i+1 < len(units) {
			n = 2
		}

		c := make([]byte, 0, 2*n)
		for _, u := range units[i : i+n] {
			c = append(c, byte(u>>8), byte(u))
		}

		chars = append(chars, c)
		i += n - 1
	}

	if 2*len(units) <= maxUserDataOctets {
		return []TextPart{ucs2Part(nil, flatten(chars))}, nil
	}

	chunks := split(chars, maxUserDataOctets-concatHeaderOctets)
	if len(chunks) > maxParts {
		return nil, ErrTextTooLong
	}

	parts := make([]TextPart, len(chunks))
	for i, chunk := range chunks {
		parts[i] = ucs2Part(concatHeader(reference, uint8(len(chunks)), uint8(i+1)), chunk)
	}

	return parts, nil
}

func ucs2Part(header, octets []byte) TextPart {
	ud := append(append([]byte(nil), header...), octets...)

	return TextPart{UserDataHeader: header != nil, DataCodingScheme: dcsUCS2, UserDataLength: uint8(len(ud)), UserData: ud}
}

func concatHeader(reference, total, part uint8) []byte {
	return []byte{concatHeaderOctets - 1, ieiConcat8, 3, reference, total, part}
}

func split(chars [][]byte, limit int) [][]byte {
	var (
		chunks  [][]byte
		current []byte
	)

	for _, c := range chars {
		if len(current)+len(c) > limit {
			chunks = append(chunks, current)
			current = nil
		}

		current = append(current, c...)
	}

	return append(chunks, current)
}

func flatten(chars [][]byte) []byte {
	var out []byte
	for _, c := range chars {
		out = append(out, c...)
	}

	return out
}

type Content struct {
	Encoding      Encoding
	Text          string
	Concatenation *Concatenation
}

func (s Submit) Content() (Content, error) {
	var (
		c      Content
		header []byte
	)

	if s.UserDataHeader {
		if len(s.UserData) == 0 || int(s.UserData[0])+1 > len(s.UserData) {
			return Content{}, fmt.Errorf("user data header: %w", errTruncated)
		}

		header = s.UserData[:1+int(s.UserData[0])]
		c.Concatenation = concatenation(header[1:])
	}

	switch {
	case isSeptetCoded(s.DataCodingScheme):
		c.Encoding = EncodingGSM7
		c.Text = decodeGSM7(s.UserData, len(header), int(s.UserDataLength))
	case isUCS2(s.DataCodingScheme):
		c.Encoding = EncodingUCS2
		c.Text = decodeUCS2(s.UserData[len(header):])
	default:
		c.Encoding = EncodingBinary
	}

	return c, nil
}

func concatenation(ies []byte) *Concatenation {
	var last *Concatenation

	for len(ies) > 0 {
		if len(ies) < 2 || len(ies) < 2+int(ies[1]) {
			return nil
		}

		iei, length := ies[0], int(ies[1])
		data := ies[2 : 2+length]

		switch {
		case iei == ieiConcat8 && length == 3:
			last = &Concatenation{Reference: uint16(data[0]), Total: data[1], Part: data[2]}
		case iei == ieiConcat16 && length == 4:
			last = &Concatenation{Reference: uint16(data[0])<<8 | uint16(data[1]), Total: data[2], Part: data[3]}
		}

		ies = ies[2+length:]
	}

	if last == nil || last.Total == 0 || last.Part == 0 || last.Part > last.Total {
		return nil
	}

	return last
}

func decodeGSM7(ud []byte, headerOctets, udl int) string {
	headerSeptets := (headerOctets*8 + 6) / 7

	runes := make([]rune, 0, udl)
	escaped := false

	for i := headerSeptets; i < udl; i++ {
		bit := i * 7
		if bit/8 >= len(ud) {
			break
		}

		s := ud[bit/8] >> (bit % 8)
		if bit%8 > 1 && bit/8+1 < len(ud) {
			s |= ud[bit/8+1] << (8 - bit%8)
		}

		s &= 0x7f

		switch {
		case escaped && s == gsm7Escape:
			escaped = false

			runes = append(runes, ' ')
		case escaped:
			escaped = false

			if r, ok := gsm7Extension[s]; ok {
				runes = append(runes, r)
			} else {
				runes = append(runes, gsm7Basic[s])
			}
		case s == gsm7Escape:
			escaped = true
		default:
			runes = append(runes, gsm7Basic[s])
		}
	}

	return string(runes)
}

func decodeUCS2(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
	}

	return string(utf16.Decode(units))
}
