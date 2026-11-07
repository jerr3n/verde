package build

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// Type ids from https://dom.rojo.space/attributes. They aren't the binary place format's.
const (
	attrString         = 0x02
	attrBool           = 0x03
	attrFloat64        = 0x06
	attrUDim           = 0x09
	attrUDim2          = 0x0A
	attrBrickColor     = 0x0E
	attrColor3         = 0x0F
	attrVector2        = 0x10
	attrVector3        = 0x11
	attrCFrame         = 0x14
	attrEnumItem       = 0x15
	attrNumberSequence = 0x17
	attrColorSequence  = 0x19
	attrNumberRange    = 0x1B
	attrRect           = 0x1C
	attrFont           = 0x21
)

// encodeAttributes turns $attributes, a list of [name, value] pairs, into the blob saved as
// AttributesSerialize. It returns nothing if there aren't any.
func (b *builder) encodeAttributes(value protocol.Value) ([]byte, error) {
	var pairs [][2]json.RawMessage
	if err := decode(value, "Attributes", "Attributes", &pairs); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}

	out := attributeWriter{}
	out.u32(uint32(len(pairs)))
	for _, pair := range pairs {
		var name string
		var attr protocol.Value
		if err := json.Unmarshal(pair[0], &name); err != nil {
			return nil, fmt.Errorf("an attribute's name isn't valid: %v", err)
		}
		if err := json.Unmarshal(pair[1], &attr); err != nil {
			return nil, fmt.Errorf("attribute %s isn't valid: %v", name, err)
		}
		out.text(name)
		if err := b.attribute(&out, attr); err != nil {
			return nil, fmt.Errorf("attribute %s: %w", name, err)
		}
	}
	return out.Bytes(), nil
}

func (b *builder) attribute(out *attributeWriter, value protocol.Value) error {
	const dataType = "attribute"

	floats := func(id byte, count int) error {
		v, err := numbers(value, value.Type, dataType, count)
		if err != nil {
			return err
		}
		out.WriteByte(id)
		out.f32s(v...)
		return nil
	}

	switch value.Type {
	case "String":
		var text string
		if err := decode(value, "String", dataType, &text); err != nil {
			return err
		}
		out.WriteByte(attrString)
		out.text(text)
	case "Bool":
		var v bool
		if err := decode(value, "Bool", dataType, &v); err != nil {
			return err
		}
		out.WriteByte(attrBool)
		if v {
			out.WriteByte(1)
		} else {
			out.WriteByte(0)
		}
	case "Number":
		var v float64
		if err := decode(value, "Number", dataType, &v); err != nil {
			return err
		}
		out.WriteByte(attrFloat64)
		binary.Write(out, binary.LittleEndian, v)
	case "UDim":
		v, err := numbers(value, "UDim", dataType, 2)
		if err != nil {
			return err
		}
		out.WriteByte(attrUDim)
		out.udim(v[0], v[1])
	case "UDim2":
		v, err := numbers(value, "UDim2", dataType, 4)
		if err != nil {
			return err
		}
		out.WriteByte(attrUDim2)
		out.udim(v[0], v[1])
		out.udim(v[2], v[3])
	case "BrickColor":
		var v float64
		if err := decode(value, "BrickColor", dataType, &v); err != nil {
			return err
		}
		out.WriteByte(attrBrickColor)
		out.u32(uint32(v))
	case "Color3":
		return floats(attrColor3, 3)
	case "Vector2":
		return floats(attrVector2, 2)
	case "Vector3":
		return floats(attrVector3, 3)
	case "NumberRange":
		return floats(attrNumberRange, 2)
	case "Rect":
		return floats(attrRect, 4)
	case "CFrame":
		v, err := numbers(value, "CFrame", dataType, 12)
		if err != nil {
			return err
		}
		out.WriteByte(attrCFrame)
		out.f32s(v[:3]...)
		out.WriteByte(0) // the rotation is stored in full, not as an id for a right-angled one
		out.f32s(v[3:]...)
	case "Enum":
		var item []string
		if err := decode(value, "Enum", dataType, &item); err != nil {
			return err
		}
		if len(item) != 2 {
			return errors.New("an Enum needs a type and an item")
		}
		number, err := b.enumNumber(item[0], item[1])
		if err != nil {
			return err
		}
		out.WriteByte(attrEnumItem)
		out.text(item[0])
		out.u32(uint32(number))
	case "NumberSequence":
		v, err := keypoints(value, "NumberSequence", dataType, 3)
		if err != nil {
			return err
		}
		out.WriteByte(attrNumberSequence)
		out.u32(uint32(len(v)))
		for _, k := range v {
			out.f32s(k[2], k[0], k[1]) // saved as envelope, time, value
		}
	case "ColorSequence":
		v, err := keypoints(value, "ColorSequence", dataType, 4)
		if err != nil {
			return err
		}
		out.WriteByte(attrColorSequence)
		out.u32(uint32(len(v)))
		for _, k := range v {
			out.f32s(0, k[0], k[1], k[2], k[3]) // an envelope that's never used comes first
		}
	case "Font":
		family, weight, style, err := b.font(value, dataType)
		if err != nil {
			return err
		}
		styleNumber, err := b.enumNumber("FontStyle", style)
		if err != nil {
			return err
		}
		out.WriteByte(attrFont)
		binary.Write(out, binary.LittleEndian, uint16(weight))
		out.WriteByte(byte(styleNumber))
		out.text(family)
		out.text("") // CachedFaceId is always there, and can be empty
	default:
		return fmt.Errorf("%s attributes aren't supported", value.Type)
	}
	return nil
}

// attributeWriter adds the format's little-endian types to a buffer. A bytes.Buffer never
// fails to write, so these don't return errors.
type attributeWriter struct{ bytes.Buffer }

func (w *attributeWriter) u32(v uint32) {
	binary.Write(w, binary.LittleEndian, v)
}

func (w *attributeWriter) f32s(values ...float64) {
	for _, v := range values {
		binary.Write(w, binary.LittleEndian, math.Float32bits(float32(v)))
	}
}

func (w *attributeWriter) udim(scale, offset float64) {
	w.f32s(scale)
	binary.Write(w, binary.LittleEndian, int32(offset))
}

func (w *attributeWriter) text(s string) {
	w.u32(uint32(len(s)))
	w.WriteString(s)
}
