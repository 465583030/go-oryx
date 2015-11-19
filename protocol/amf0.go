// The MIT License (MIT)
//
// Copyright (c) 2013-2015 Oryx(ossrs)
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

package protocol

import (
	"bytes"
	"encoding"
	"encoding/binary"
)

// AMF0 marker
const (
	MarkerAmf0Number      = 0x00
	MarkerAmf0Boolean     = 0x01
	MarkerAmf0String      = 0x02
	MarkerAmf0Object      = 0x03
	MarkerAmf0MovieClip   = 0x04 // reserved, not supported
	MarkerAmf0Null        = 0x05
	MarkerAmf0Undefined   = 0x06
	MarkerAmf0Reference   = 0x07
	MarkerAmf0EcmaArray   = 0x08
	MarkerAmf0ObjectEnd   = 0x09
	MarkerAmf0StrictArray = 0x0A
	MarkerAmf0Date        = 0x0B
	MarkerAmf0LongString  = 0x0C
	MarkerAmf0UnSupported = 0x0D
	MarkerAmf0RecordSet   = 0x0E // reserved, not supported
	MarkerAmf0XmlDocument = 0x0F
	MarkerAmf0TypedObject = 0x10
	// AVM+ object is the AMF3 object.
	MarkerAmf0AVMplusObject = 0x11
	// origin array whos data takes the same form as LengthValueBytes
	MarkerAmf0OriginStrictArray = 0x20

	// User defined
	MarkerAmf0Invalid = 0x3F
)

// the amf0 type interface
type Amf0Any interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler

	// the total size of bytes for this amf0 instance.
	Size() int
}

// discovery the Amf0Any type by marker.
func Amf0Discovery(data []byte) (a Amf0Any, err error) {
	if len(data) == 0 {
		return nil, Amf0Error
	}

	switch data[0] {
	case MarkerAmf0String:
		var o Amf0String
		return &o, nil
	case MarkerAmf0Invalid:
		fallthrough
	default:
		return nil, Amf0Error
	}
}

// a amf0 string is a string.
type Amf0String string

// Amf0Any
func (s *Amf0String) Size() int {
	return 1 + 2 + len(*s)
}

// encoding.BinaryMarshaler
func (s *Amf0String) MarshalBinary() (data []byte, err error) {
	var b bytes.Buffer

	if err = b.WriteByte(MarkerAmf0String); err != nil {
		return
	}

	if err = binary.Write(&b, binary.BigEndian, uint16(len(*s))); err != nil {
		return
	}

	if _, err = b.Write(([]byte)(*s)); err != nil {
		return
	}

	return b.Bytes(), nil
}

// encoding.BinaryUnmarshaler
func (s *Amf0String) UnmarshalBinary(data []byte) (err error) {
	b := bytes.NewBuffer(data)

	var m byte
	if m, err = b.ReadByte(); err != nil {
		return
	}

	if m != MarkerAmf0String {
		return Amf0Error
	}

	var nb uint16
	if err = binary.Read(b, binary.BigEndian, &nb); err != nil {
		return
	}

	v := make([]byte, nb)
	if _, err = b.Read(v); err != nil {
		return
	}
	*s = Amf0String(string(v))

	return
}
