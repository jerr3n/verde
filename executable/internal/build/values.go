package build

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// property returns the element for one property, and the name it's saved under. An empty
// element means there's nothing to write, like a Nil, which a new instance already has.
func (b *builder) property(inst protocol.Instance, name string, value protocol.Value) (element, saved string, err error) {
	switch name {
	case "$tags":
		element, err = tagsElement(value)
		return element, "Tags", err
	case "$attributes":
		data, err := b.encodeAttributes(value)
		if err != nil {
			return "", "", err
		}
		if len(data) == 0 {
			return "", "", nil
		}
		return binaryElement("AttributesSerialize", data), "AttributesSerialize", nil
	}

	saved, dataType, ok := b.db.savedAs(inst.Class, name)
	if !ok {
		return "", "", errors.New("Roblox doesn't save this property, or the reflection database doesn't know it")
	}
	if !validText(saved) {
		return "", "", errors.New("its name has a character XML can't hold")
	}
	element, err = b.element(saved, dataType, value)
	return element, saved, err
}

// element converts a wire value to the type the database says the property has.
func (b *builder) element(name, dataType string, value protocol.Value) (string, error) {
	if value.Type == "Nil" {
		return b.nilElement(name, dataType), nil
	}

	switch dataType {
	case "Bool":
		var v bool
		if err := decode(value, "Bool", dataType, &v); err != nil {
			return "", err
		}
		return tag("bool", name, strconv.FormatBool(v)), nil

	case "String":
		text, err := decodeText(value, dataType)
		if err != nil {
			return "", err
		}
		if name == "Source" {
			return tag("ProtectedString", name, protected(text)), nil
		}
		return tag("string", name, escape(text)), nil

	case "BinaryString":
		text, err := decodeText(value, dataType)
		if err != nil {
			return "", err
		}
		return binaryElement(name, []byte(text)), nil

	case "Float32", "Float64":
		var v float64
		if err := decode(value, "Number", dataType, &v); err != nil {
			return "", err
		}
		if dataType == "Float32" {
			return tag("float", name, float32Text(v)), nil
		}
		return tag("double", name, strconv.FormatFloat(v, 'g', -1, 64)), nil

	case "Int32", "Int64":
		if value.Type != "Number" {
			return "", wrongType(value, dataType)
		}
		bits, element := 32, "int"
		if dataType == "Int64" {
			bits, element = 64, "int64"
		}
		text, err := integerText(value.Value, bits)
		if err != nil {
			return "", err
		}
		return tag(element, name, text), nil

	case "Enum":
		number, err := b.enumValue(value)
		if err != nil {
			return "", err
		}
		return tag("token", name, strconv.FormatInt(number, 10)), nil

	case "Ref":
		var target protocol.Ref
		if err := decode(value, "Ref", dataType, &target); err != nil {
			return "", err
		}
		referent, ok := b.referents[target]
		if !ok {
			return "", fmt.Errorf("it points at %s, which isn't in the project", target)
		}
		return tag("Ref", name, referent), nil

	case "Content", "ContentId":
		return contentElement(name, dataType, value)

	case "BrickColor":
		var v float64
		if err := decode(value, "BrickColor", dataType, &v); err != nil {
			return "", err
		}
		return tag("int", name, strconv.FormatInt(int64(v), 10)), nil

	case "Vector2":
		return floatsElement("Vector2", name, value, []string{"X", "Y"})
	case "Vector3":
		return floatsElement("Vector3", name, value, []string{"X", "Y", "Z"})
	case "CFrame":
		v, err := numbers(value, "CFrame", dataType, 12)
		if err != nil {
			return "", err
		}
		return tag("CoordinateFrame", name, children(
			[]string{"X", "Y", "Z", "R00", "R01", "R02", "R10", "R11", "R12", "R20", "R21", "R22"}, v)), nil
	case "Color3":
		return floatsElement("Color3", name, value, []string{"R", "G", "B"})
	case "Color3uint8":
		v, err := numbers(value, "Color3", dataType, 3)
		if err != nil {
			return "", err
		}
		packed := uint32(0xFF000000) | uint32(colorByte(v[0]))<<16 | uint32(colorByte(v[1]))<<8 | uint32(colorByte(v[2]))
		return tag("Color3uint8", name, strconv.FormatUint(uint64(packed), 10)), nil

	case "UDim":
		v, err := numbers(value, "UDim", dataType, 2)
		if err != nil {
			return "", err
		}
		return tag("UDim", name, "<S>"+float32Text(v[0])+"</S><O>"+intText(v[1])+"</O>"), nil
	case "UDim2":
		v, err := numbers(value, "UDim2", dataType, 4)
		if err != nil {
			return "", err
		}
		return tag("UDim2", name, "<XS>"+float32Text(v[0])+"</XS><XO>"+intText(v[1])+"</XO>"+
			"<YS>"+float32Text(v[2])+"</YS><YO>"+intText(v[3])+"</YO>"), nil
	case "Rect":
		v, err := numbers(value, "Rect", dataType, 4)
		if err != nil {
			return "", err
		}
		xy := []string{"X", "Y"}
		return tag("Rect2D", name, "<min>"+children(xy, v[:2])+"</min><max>"+children(xy, v[2:])+"</max>"), nil

	case "NumberRange":
		v, err := numbers(value, "NumberRange", dataType, 2)
		if err != nil {
			return "", err
		}
		return tag("NumberRange", name, float32Text(v[0])+" "+float32Text(v[1])+" "), nil
	case "NumberSequence":
		keypoints, err := keypoints(value, "NumberSequence", dataType, 3)
		if err != nil {
			return "", err
		}
		var text strings.Builder
		for _, k := range keypoints {
			fmt.Fprintf(&text, "%s %s %s ", float32Text(k[0]), float32Text(k[1]), float32Text(k[2]))
		}
		return tag("NumberSequence", name, text.String()), nil
	case "ColorSequence":
		keypoints, err := keypoints(value, "ColorSequence", dataType, 4)
		if err != nil {
			return "", err
		}
		var text strings.Builder
		for _, k := range keypoints {
			// The last number is an envelope, which colors have no use for.
			fmt.Fprintf(&text, "%s %s %s %s 0 ", float32Text(k[0]), float32Text(k[1]), float32Text(k[2]), float32Text(k[3]))
		}
		return tag("ColorSequence", name, text.String()), nil

	case "Font":
		family, weight, style, err := b.font(value, dataType)
		if err != nil {
			return "", err
		}
		return tag("Font", name, "<Family><url>"+escape(family)+"</url></Family>"+
			"<Weight>"+strconv.FormatInt(weight, 10)+"</Weight><Style>"+escape(style)+"</Style>"), nil

	case "PhysicalProperties":
		v, err := numbers(value, "PhysicalProperties", dataType, 5)
		if err != nil {
			return "", err
		}
		return tag("PhysicalProperties", name, "<CustomPhysics>true</CustomPhysics>"+children(
			[]string{"Density", "Friction", "Elasticity", "FrictionWeight", "ElasticityWeight"}, v)), nil
	}
	return "", fmt.Errorf("%s properties aren't supported yet", dataType)
}

// nilElement writes an empty value where the type has a way to say so.
func (b *builder) nilElement(name, dataType string) string {
	switch dataType {
	case "Ref":
		return tag("Ref", name, "null")
	case "Content", "ContentId":
		return tag(dataType, name, "<null></null>")
	case "PhysicalProperties":
		return tag(dataType, name, "<CustomPhysics>false</CustomPhysics>")
	}
	return ""
}

// contentElement accepts both wire types, since a ContentId reads as a string in Studio.
func contentElement(name, dataType string, value protocol.Value) (string, error) {
	var text string
	if value.Type != "Content" && value.Type != "String" {
		return "", wrongType(value, dataType)
	}
	if len(value.Value) > 0 {
		if err := json.Unmarshal(value.Value, &text); err != nil {
			return "", err
		}
	}
	if !validText(text) {
		return "", errors.New("it has a character XML can't hold")
	}
	switch {
	case text == "":
		return tag(dataType, name, "<null></null>"), nil
	case dataType == "Content":
		return tag(dataType, name, "<uri>"+escape(text)+"</uri>"), nil
	}
	return tag(dataType, name, "<url>"+escape(text)+"</url>"), nil
}

func tagsElement(value protocol.Value) (string, error) {
	var tags []string
	if err := decode(value, "Tags", "Tags", &tags); err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", nil
	}
	return binaryElement("Tags", []byte(strings.Join(tags, "\x00"))), nil
}

func (b *builder) enumValue(value protocol.Value) (int64, error) {
	var item []string
	if err := decode(value, "Enum", "Enum", &item); err != nil {
		return 0, err
	}
	if len(item) != 2 {
		return 0, errors.New("an Enum needs a type and an item")
	}
	return b.enumNumber(item[0], item[1])
}

func (b *builder) enumNumber(enum, item string) (int64, error) {
	number, ok := b.db.Enums[enum][item]
	if !ok {
		return 0, fmt.Errorf("Enum.%s.%s doesn't exist", enum, item)
	}
	return number, nil
}

// font returns a Font's family, weight number and style name.
func (b *builder) font(value protocol.Value, dataType string) (family string, weight int64, style string, err error) {
	var parts []string
	if err := decode(value, "Font", dataType, &parts); err != nil {
		return "", 0, "", err
	}
	if len(parts) != 3 {
		return "", 0, "", errors.New("a Font needs a family, a weight and a style")
	}
	if !validText(parts[0]) {
		return "", 0, "", errors.New("it has a character XML can't hold")
	}
	if weight, err = b.enumNumber("FontWeight", parts[1]); err != nil {
		return "", 0, "", err
	}
	if _, err = b.enumNumber("FontStyle", parts[2]); err != nil {
		return "", 0, "", err
	}
	return parts[0], weight, parts[2], nil
}

func tag(element, name, body string) string {
	return "<" + element + " name=\"" + escape(name) + "\">" + body + "</" + element + ">"
}

func binaryElement(name string, data []byte) string {
	return tag("BinaryString", name, base64.StdEncoding.EncodeToString(data))
}

// protected writes a script's text. CDATA keeps it readable, but an XML reader turns \r\n
// into \n even inside CDATA, so text with \r has to be escaped instead.
func protected(text string) string {
	if strings.Contains(text, "\r") {
		return escape(text)
	}
	return "<![CDATA[" + strings.ReplaceAll(text, "]]>", "]]]]><![CDATA[>") + "]]>"
}

func floatsElement(element, name string, value protocol.Value, fields []string) (string, error) {
	v, err := numbers(value, element, element, len(fields))
	if err != nil {
		return "", err
	}
	return tag(element, name, children(fields, v)), nil
}

func children(fields []string, values []float64) string {
	var out strings.Builder
	for i, field := range fields {
		out.WriteString("<" + field + ">" + float32Text(values[i]) + "</" + field + ">")
	}
	return out.String()
}

func float32Text(v float64) string {
	switch single := float32(v); {
	case math.IsInf(float64(single), 1):
		return "INF" // a double too big for a float, and Go would write +Inf
	case math.IsInf(float64(single), -1):
		return "-INF"
	default:
		return strconv.FormatFloat(float64(single), 'g', -1, 32)
	}
}

func intText(v float64) string {
	return strconv.FormatInt(int64(v), 10)
}

// integerText reads the number's own text where it can, since a float64 would round a large
// Int64. Studio can write these as 1e+15, so that form has to work too.
func integerText(raw json.RawMessage, bits int) (string, error) {
	text := string(raw)
	if n, err := strconv.ParseInt(text, 10, bits); err == nil {
		return strconv.FormatInt(n, 10), nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return "", fmt.Errorf("%s isn't a number", text)
	}
	limit := math.Ldexp(1, bits-1)
	if f != math.Trunc(f) || f < -limit || f >= limit {
		return "", fmt.Errorf("%s doesn't fit a %d-bit integer", text, bits)
	}
	return strconv.FormatInt(int64(f), 10), nil
}

func colorByte(v float64) uint8 {
	return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255))
}

func decode(value protocol.Value, wire, dataType string, out any) error {
	if value.Type != wire {
		return wrongType(value, dataType)
	}
	if err := json.Unmarshal(value.Value, out); err != nil {
		return fmt.Errorf("a %s value isn't valid: %v", wire, err)
	}
	return nil
}

func decodeText(value protocol.Value, dataType string) (string, error) {
	var text string
	if err := decode(value, "String", dataType, &text); err != nil {
		return "", err
	}
	if !validText(text) {
		return "", errors.New("it has a character XML can't hold")
	}
	return text, nil
}

func numbers(value protocol.Value, wire, dataType string, count int) ([]float64, error) {
	var v []float64
	if err := decode(value, wire, dataType, &v); err != nil {
		return nil, err
	}
	if len(v) != count {
		return nil, fmt.Errorf("a %s needs %d numbers, not %d", wire, count, len(v))
	}
	return v, nil
}

func keypoints(value protocol.Value, wire, dataType string, size int) ([][]float64, error) {
	var v [][]float64
	if err := decode(value, wire, dataType, &v); err != nil {
		return nil, err
	}
	for _, k := range v {
		if len(k) != size {
			return nil, fmt.Errorf("a %s keypoint needs %d numbers, not %d", wire, size, len(k))
		}
	}
	return v, nil
}

func wrongType(value protocol.Value, dataType string) error {
	return fmt.Errorf("it holds a %s, but the property is a %s", value.Type, dataType)
}
