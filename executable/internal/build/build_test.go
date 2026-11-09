// SPDX-License-Identifier: MPL-2.0

package build

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/project"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

func wire(t string, v any) protocol.Value {
	if v == nil {
		return protocol.Value{Type: t}
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return protocol.Value{Type: t, Value: encoded}
}

// newTree makes a tree from instances. The root is a DataModel with the name "game".
func newTree(t *testing.T, instances ...protocol.Instance) *tree.Tree {
	t.Helper()
	out := tree.New(protocol.Instance{ID: "game", Name: "game", Class: "DataModel"})
	for _, inst := range instances {
		if inst.Parent == "" {
			inst.Parent = "game"
		}
		if err := out.Insert(inst); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func build(t *testing.T, tr *tree.Tree) (string, []string) {
	t.Helper()
	var out bytes.Buffer
	result, err := Write(&out, tr)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out.String(), result.Warnings
}

func onePart(t *testing.T, class string, props map[string]protocol.Value) (string, []string) {
	t.Helper()
	return build(t, newTree(t,
		protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"},
		protocol.Instance{ID: "x", Parent: "ws", Name: "X", Class: class, Props: props},
	))
}

func TestPropertiesUseTheirSavedNameAndType(t *testing.T) {
	tests := []struct {
		name, class, prop string
		value             protocol.Value
		want              string
	}{
		{"renamed", "Part", "Size", wire("Vector3", []float64{4, 1, 2}),
			`<Vector3 name="size"><X>4</X><Y>1</Y><Z>2</Z></Vector3>`},
		{"renamed to another type", "Part", "Color", wire("Color3", []float64{96.0 / 255, 64.0 / 255, 32.0 / 255}),
			`<Color3uint8 name="Color3uint8">4284497952</Color3uint8>`},
		{"enum number", "Part", "Material", wire("Enum", []string{"Material", "Plastic"}),
			`<token name="Material">256</token>`},
		{"float", "Part", "Transparency", wire("Number", 0.5), `<float name="Transparency">0.5</float>`},
		{"double", "NumberValue", "Value", wire("Number", 0.1), `<double name="Value">0.1</double>`},
		{"int", "TextLabel", "LayoutOrder", wire("Number", 3), `<int name="LayoutOrder">3</int>`},
		{"int64 written as a float", "IntValue", "Value", wire("Number", 1e15), `<int64 name="Value">1000000000000000</int64>`},
		{"brick color", "Part", "BrickColor", wire("BrickColor", 194), `<int name="BrickColor">194</int>`},
		{"content id", "Decal", "Texture", wire("String", "rbxassetid://1"),
			`<ContentId name="Texture"><url>rbxassetid://1</url></ContentId>`},
		{"content", "Decal", "TextureContent", wire("Content", "rbxassetid://1"),
			`<Content name="TextureContent"><uri>rbxassetid://1</uri></Content>`},
		{"empty content", "Decal", "TextureContent", wire("Content", nil),
			`<Content name="TextureContent"><null></null></Content>`},
		{"empty physics", "Part", "CustomPhysicalProperties", wire("Nil", nil),
			`<PhysicalProperties name="CustomPhysicalProperties"><CustomPhysics>false</CustomPhysics></PhysicalProperties>`},
		{"font", "TextLabel", "FontFace", wire("Font", []string{"rbxasset://fonts/families/Arial.json", "Bold", "Italic"}),
			`<Font name="FontFace"><Family><url>rbxasset://fonts/families/Arial.json</url></Family><Weight>700</Weight><Style>Italic</Style></Font>`},
		{"escaped text", "TextLabel", "Text", wire("String", `a<b & "c"`),
			`<string name="Text">a&lt;b &amp; &#34;c&#34;</string>`},
		{"tags", "Part", "$tags", wire("Tags", []string{"A", "B"}),
			`<BinaryString name="Tags">` + base64.StdEncoding.EncodeToString([]byte("A\x00B")) + `</BinaryString>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, warnings := onePart(t, test.class, map[string]protocol.Value{test.prop: test.value})
			if !strings.Contains(out, test.want) {
				t.Errorf("missing %s in:\n%s", test.want, out)
			}
			if len(warnings) > 0 {
				t.Errorf("warnings: %v", warnings)
			}
		})
	}
}

func TestSourceKeepsItsExactText(t *testing.T) {
	tests := []struct{ name, source, want string }{
		{"cdata", "print('<hi>')\n", "<![CDATA[print('<hi>')\n]]>"},
		{"cdata end inside", "a[b[1]]>c", "<![CDATA[a[b[1]]]]><![CDATA[>c]]>"},
		{"carriage return", "a\r\nb", "a&#xD;&#xA;b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, _ := onePart(t, "Script", map[string]protocol.Value{"Source": wire("String", test.source)})
			want := `<ProtectedString name="Source">` + test.want + `</ProtectedString>`
			if !strings.Contains(out, want) {
				t.Errorf("missing %s in:\n%s", want, out)
			}
		})
	}
}

func TestSourceXMLCantHoldFailsTheBuild(t *testing.T) {
	tr := newTree(t,
		protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"},
		protocol.Instance{ID: "x", Parent: "ws", Name: "Main", Class: "Script",
			Props: map[string]protocol.Value{"Source": wire("String", "print('\x01')")}},
	)
	_, err := Write(&bytes.Buffer{}, tr)
	if err == nil || !strings.Contains(err.Error(), "Workspace/Main") {
		t.Fatalf("err = %v, want one naming Workspace/Main", err)
	}
}

func TestPropertyProblemsWarnOncePerKind(t *testing.T) {
	bad := map[string]protocol.Value{
		"NotARealProperty": wire("Bool", true),
		"Size":             wire("Vector3", []float64{1, 2}),
		"Anchored":         wire("Number", 1),
	}
	out, warnings := build(t, newTree(t,
		protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"},
		protocol.Instance{ID: "a", Parent: "ws", Name: "A", Class: "Part", Props: bad},
		protocol.Instance{ID: "b", Parent: "ws", Name: "B", Class: "Part", Props: bad},
	))
	if len(warnings) != 3 {
		t.Fatalf("warnings = %q, want 3", warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"Part.NotARealProperty", "Part.Size", "Part.Anchored", "Workspace/A and 1 more"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(out, "NotARealProperty") {
		t.Errorf("wrote the unknown property:\n%s", out)
	}
}

func TestUnknownClassKeepsTheInstance(t *testing.T) {
	out, warnings := onePart(t, "ClassFromTheFuture", map[string]protocol.Value{"Thing": wire("Bool", true)})
	if !strings.Contains(out, `class="ClassFromTheFuture"`) {
		t.Errorf("instance is missing:\n%s", out)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %q, want 1", warnings)
	}
}

func TestRefPointsAtReferent(t *testing.T) {
	out, warnings := build(t, newTree(t,
		protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"},
		protocol.Instance{ID: "car", Parent: "ws", Name: "Car", Class: "Model",
			Props: map[string]protocol.Value{"PrimaryPart": wire("Ref", "wheel")}},
		protocol.Instance{ID: "wheel", Parent: "car", Name: "Wheel", Class: "Part"},
		protocol.Instance{ID: "gone", Parent: "ws", Name: "Gone", Class: "ObjectValue",
			Props: map[string]protocol.Value{"Value": wire("Ref", "missing")}},
	))
	match := regexp.MustCompile(`<Ref name="PrimaryPart">(RBX[0-9A-F]+)</Ref>`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no PrimaryPart in:\n%s", out)
	}
	if !strings.Contains(out, `<Item class="Part" referent="`+match[1]+`">`) {
		t.Errorf("PrimaryPart points at %s, which isn't the Part:\n%s", match[1], out)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "missing") {
		t.Errorf("warnings = %q, want one for the missing target", warnings)
	}
}

func TestSameTreeGivesSameBytes(t *testing.T) {
	make := func() *tree.Tree {
		return newTree(t,
			protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"},
			protocol.Instance{ID: "a", Parent: "ws", Name: "A", Class: "Part", Props: map[string]protocol.Value{
				"Anchored": wire("Bool", true), "Size": wire("Vector3", []float64{1, 2, 3}), "Locked": wire("Bool", true),
			}},
		)
	}
	first, _ := build(t, make())
	// Go map order is random. Thus 20 builds find output that depends on map order.
	for range 20 {
		if again, _ := build(t, make()); again != first {
			t.Fatalf("output changed:\n%s\n---\n%s", first, again)
		}
	}
}

func TestAttributeBytesMatchTheFormatsExamples(t *testing.T) {
	font := "90 01 00 2C 00 00 00 72 62 78 61 73 73 65 74 3A 2F 2F 66 6F 6E 74 73 2F 66 61 6D 69 6C 69 65 73 2F 53 6F 75 72 63 65 53 61 6E 73 50 72 6F 2E 6A 73 6F 6E 00 00 00 00"
	tests := []struct {
		name  string
		value protocol.Value
		// want is the example from https://dom.rojo.space/attributes.
		want string
	}{
		{"UDim", wire("UDim", []float64{123, 456}), "09 00 00 f6 42 c8 01 00 00"},
		{"UDim2", wire("UDim2", []float64{1, 2, 3, 4}), "0a 00 00 80 3f 02 00 00 00 00 00 40 40 04 00 00 00"},
		{"Vector2", wire("Vector2", []float64{10, 20}), "10 00 00 20 41 00 00 a0 41"},
		{"Vector3", wire("Vector3", []float64{10, 20, 30}), "11 00 00 20 41 00 00 a0 41 00 00 f0 41"},
		{"Rect", wire("Rect", []float64{10, 20, 30, 40}), "1c 00 00 20 41 00 00 a0 41 00 00 f0 41 00 00 20 42"},
		{"NumberSequence", wire("NumberSequence", [][]float64{{0, 0, 0}, {0.5, 1, 0}, {1, 1, 0.5}}),
			"17 03 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 3f 00 00 80 3f 00 00 00 3f 00 00 80 3f 00 00 80 3f"},
		{"ColorSequence", wire("ColorSequence", [][]float64{{0, 1, 0, 0}, {0.5, 0, 1, 0}, {1, 0, 0, 1}}),
			"19 03 00 00 00 00 00 00 00 00 00 00 00 00 00 80 3f 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 3f 00 00 00 00 00 00 80 3f 00 00 00 00 00 00 00 00 00 00 80 3f 00 00 00 00 00 00 00 00 00 00 80 3f"},
		// The example has a CachedFaceId, but the wire format does not have that field.
		{"Font", wire("Font", []string{"rbxasset://fonts/families/SourceSansPro.json", "Regular", "Normal"}), "21 " + font},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := &builder{db: database()}
			out := attributeWriter{}
			if err := b.attribute(&out, test.value); err != nil {
				t.Fatal(err)
			}
			want, err := hex.DecodeString(strings.ReplaceAll(test.want, " ", ""))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Errorf("got  % x\nwant % x", out.Bytes(), want)
			}
		})
	}
}

func TestWriteFile(t *testing.T) {
	tr := newTree(t, protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"})
	dir := t.TempDir()

	path := filepath.Join(dir, "game.rbxlx")
	if _, err := WriteFile(path, tr, nil, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), `<roblox version="4">`) {
		t.Errorf("unexpected file: %s", data)
	}

	for _, bad := range []string{"game.rbxl", "game.rbxm", "game"} {
		if _, err := WriteFile(filepath.Join(dir, bad), tr, nil, ""); err == nil {
			t.Errorf("WriteFile(%s) worked, want an error", bad)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left files behind: %v", entries)
	}
}

// everyType has a property of each wire type, on classes that have that property in Roblox.
func everyType() []protocol.Instance {
	attributes := wire("Attributes", [][2]any{
		{"ABool", wire("Bool", true)},
		{"AColor", wire("Color3", []float64{0.25, 0.5, 0.75})},
		{"AFrame", wire("CFrame", []float64{1, 2, 3, 0, 0, 1, 0, 1, 0, -1, 0, 0})},
		{"ANumber", wire("Number", 1.5)},
		{"ARange", wire("NumberRange", []float64{1, 2})},
		{"ARect", wire("Rect", []float64{1, 2, 3, 4})},
		{"ASequence", wire("NumberSequence", [][]float64{{0, 1, 0}, {1, 2, 0}})},
		{"AString", wire("String", "hi")},
		{"AVector", wire("Vector3", []float64{1, 2, 3})},
		{"Gradient", wire("ColorSequence", [][]float64{{0, 1, 0, 0}, {1, 0, 0, 1}})},
		{"Offset", wire("UDim", []float64{0.5, 3})},
		{"Position", wire("UDim2", []float64{0.5, 3, 0.25, 4})},
		{"Stone", wire("BrickColor", 194)},
		{"Vec2", wire("Vector2", []float64{1, 2})},
	})
	props := func(pairs ...any) map[string]protocol.Value {
		out := map[string]protocol.Value{}
		for i := 0; i < len(pairs); i += 2 {
			out[pairs[i].(string)] = pairs[i+1].(protocol.Value)
		}
		return out
	}
	return []protocol.Instance{
		{ID: "ws", Name: "Workspace", Class: "Workspace"},
		{ID: "gui", Name: "StarterGui", Class: "StarterGui"},
		{ID: "sss", Name: "ServerScriptService", Class: "ServerScriptService"},
		{ID: "main", Parent: "sss", Name: "Main", Class: "Script", Props: props(
			"Source", wire("String", "print('<hi> & [[x]]>')\n\tprint(\"tab\")\n"),
			"RunContext", wire("Enum", []string{"RunContext", "Client"}),
		)},
		{ID: "crlf", Parent: "sss", Name: "Windows", Class: "ModuleScript", Props: props(
			"Source", wire("String", "  return 1\r\n\r\n"),
		)},
		{ID: "local", Parent: "sss", Name: "Local", Class: "LocalScript", Props: props("Source", wire("String", ""))},
		{ID: "car", Parent: "ws", Name: "Car", Class: "Model", Props: props(
			"PrimaryPart", wire("Ref", "lamp"),
			"ModelStreamingMode", wire("Enum", []string{"ModelStreamingMode", "Atomic"}),
		)},
		{ID: "lamp", Parent: "car", Name: "Lamp", Class: "Part", Props: props(
			"Anchored", wire("Bool", true),
			"Size", wire("Vector3", []float64{4, 1, 2}),
			"Color", wire("Color3", []float64{96.0 / 255, 64.0 / 255, 32.0 / 255}),
			"Material", wire("Enum", []string{"Material", "Neon"}),
			"Shape", wire("Enum", []string{"PartType", "Ball"}),
			"Transparency", wire("Number", 0.25),
			"CFrame", wire("CFrame", []float64{1, 2, 3, 0, 0, 1, 0, 1, 0, -1, 0, 0}),
			"CustomPhysicalProperties", wire("PhysicalProperties", []float64{1, 0.5, 0.25, 2, 3}),
			"$tags", wire("Tags", []string{"First", "Second"}),
			"$attributes", attributes,
		)},
		{ID: "weld", Parent: "car", Name: "Weld", Class: "Weld", Props: props("Part0", wire("Ref", "lamp"))},
		{ID: "value", Parent: "ws", Name: "Number", Class: "NumberValue", Props: props("Value", wire("Number", 0.1))},
		{ID: "int", Parent: "ws", Name: "Int", Class: "IntValue", Props: props("Value", wire("Number", 1234567890123))},
		{ID: "sparks", Parent: "lamp", Name: "Sparks", Class: "ParticleEmitter", Props: props(
			"Size", wire("NumberSequence", [][]float64{{0, 1, 0.5}, {1, 2, 0}}),
			"Color", wire("ColorSequence", [][]float64{{0, 1, 0, 0}, {1, 0, 0, 1}}),
			"Lifetime", wire("NumberRange", []float64{1, 2}),
			"TextureContent", wire("Content", "rbxassetid://7"),
		)},
		{ID: "decal", Parent: "lamp", Name: "Decal", Class: "Decal", Props: props("Texture", wire("String", "rbxassetid://5"))},
		{ID: "billboard", Parent: "gui", Name: "Billboard", Class: "BillboardGui", Props: props("SizeOffset", wire("Vector2", []float64{1, 2}))},
		{ID: "image", Parent: "gui", Name: "Image", Class: "ImageLabel", Props: props(
			"SliceCenter", wire("Rect", []float64{1, 2, 3, 4}),
			"Position", wire("UDim2", []float64{0.5, 3, 0.25, 4}),
		)},
		{ID: "padding", Parent: "gui", Name: "Padding", Class: "UIPadding", Props: props("PaddingLeft", wire("UDim", []float64{0.5, 3}))},
		{ID: "label", Parent: "gui", Name: "Label & <Co>", Class: "TextLabel", Props: props(
			"Text", wire("String", `  <b>"Hi" & 'bye'</b>`+"\n"),
			"LayoutOrder", wire("Number", 7),
			"FontFace", wire("Font", []string{"rbxasset://fonts/families/Arial.json", "Bold", "Italic"}),
		)},
	}
}

// TestLuneReadsWhatWasBuilt is the most important check: a place is correct only if a different program can read it.
// Lune uses the rbx-dom reader, and the values go through the encoder of the plugin.
func TestLuneReadsWhatWasBuilt(t *testing.T) {
	lune, err := exec.LookPath("lune")
	if err != nil {
		t.Skip("lune isn't installed")
	}

	t.Run("place", func(t *testing.T) {
		tr := newTree(t, everyType()...)
		place := filepath.Join(t.TempDir(), "game.rbxlx")
		result, err := WriteFile(place, tr, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		checkWithLune(t, lune, place, tr, tr.Children("game"), result)
	})

	// The Model holds Parts with each type of value, and Refs that point inside the Model.
	t.Run("model", func(t *testing.T) {
		tr := newTree(t, everyType()...)
		model := filepath.Join(t.TempDir(), "Car.rbxmx")
		result, err := WriteFile(model, tr, nil, "Workspace/Car")
		if err != nil {
			t.Fatal(err)
		}
		checkWithLune(t, lune, model, tr, []protocol.Ref{"car"}, result)
	})
}

// checkWithLune reads a built file with Lune, and compares each instance in roots and their descendants with the tree.
func checkWithLune(t *testing.T, lune, file string, tr *tree.Tree, roots []protocol.Ref, result Result) {
	t.Helper()
	if len(result.Warnings) > 0 {
		t.Errorf("warnings: %q", result.Warnings)
	}

	type wantedEntry struct {
		ID    protocol.Ref              `json:"id"`
		Path  []string                  `json:"path"`
		Class string                    `json:"class"`
		Props map[string]protocol.Value `json:"props"`
	}
	expected := map[protocol.Ref]protocol.Instance{}
	wanted := []wantedEntry{}
	var visit func(id protocol.Ref, parentPath []string)
	visit = func(id protocol.Ref, parentPath []string) {
		inst, ok := tr.Get(id)
		if !ok {
			t.Fatalf("instance %s not found", id)
		}
		path := append(append([]string{}, parentPath...), inst.Name)
		expected[id] = inst
		props := inst.Props
		if props == nil {
			props = map[string]protocol.Value{}
		}
		wanted = append(wanted, wantedEntry{ID: id, Path: path, Class: inst.Class, Props: props})
		for _, child := range tr.Children(id) {
			visit(child, path)
		}
	}
	for _, root := range roots {
		visit(root, nil)
	}
	if result.Instances != len(wanted) {
		t.Errorf("Instances = %d, want %d", result.Instances, len(wanted))
	}

	wantedJSON, err := json.Marshal(wanted)
	if err != nil {
		t.Fatal(err)
	}
	wantedPath := filepath.Join(t.TempDir(), "wanted.json")
	if err := os.WriteFile(wantedPath, wantedJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	values, err := filepath.Abs(filepath.Join("..", "..", "..", "plugin", "src", "Verde", "Values.luau"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(lune, "run", filepath.Join("testdata", "read_place.luau"), values, file, wantedPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("lune: %v\n%s", err, output)
	}

	var got struct {
		Top       int `json:"top"`
		Instances []struct {
			ID    protocol.Ref              `json:"id"`
			Class string                    `json:"class"`
			Props map[string]protocol.Value `json:"props"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("lune's output isn't JSON: %v\n%s", err, output)
	}
	if got.Top != len(roots) {
		t.Errorf("the file has %d instances at the top, want %d", got.Top, len(roots))
	}
	if len(got.Instances) != len(wanted) {
		t.Fatalf("read %d instances, want %d", len(got.Instances), len(wanted))
	}
	for _, read := range got.Instances {
		want := expected[read.ID]
		if read.Class != want.Class {
			t.Errorf("%s: class = %s, want %s", want.Name, read.Class, want.Class)
		}
		for name, value := range want.Props {
			if err := sameValue(value, read.Props[name]); err != nil {
				t.Errorf("%s.%s: %v", want.Name, name, err)
			}
		}
	}
}

// modelTree is a small game with the difficult cases for the choice of a model root.
func modelTree(t *testing.T) (*tree.Tree, []protocol.Mount) {
	t.Helper()
	tr := newTree(t,
		protocol.Instance{ID: "ws", Name: "Workspace", Class: "Workspace"},
		protocol.Instance{ID: "car", Parent: "ws", Name: "Car", Class: "Model", Props: map[string]protocol.Value{
			"PrimaryPart": wire("Ref", "wheel"),
		}},
		protocol.Instance{ID: "wheel", Parent: "car", Name: "Wheel", Class: "Part"},
		protocol.Instance{ID: "link", Parent: "car", Name: "Link", Class: "ObjectValue", Props: map[string]protocol.Value{
			"Value": wire("Ref", "other"),
		}},
		protocol.Instance{ID: "other", Parent: "ws", Name: "Other", Class: "Part"},
		protocol.Instance{ID: "twin1", Parent: "ws", Name: "Twin", Class: "Part"},
		protocol.Instance{ID: "twin2", Parent: "ws", Name: "Twin", Class: "Model"},
		protocol.Instance{ID: "slash", Parent: "ws", Name: "A/B", Class: "Part"},
	)
	return tr, []protocol.Mount{{ID: "game", Path: []string{}}, {ID: "ws", Path: []string{"Workspace"}, Sync: true}}
}

func TestModelHoldsOnlyItsInstance(t *testing.T) {
	tr, _ := modelTree(t)
	var out bytes.Buffer
	result, err := WriteModel(&out, tr, "car")
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()

	for _, want := range []string{`<Meta name="ExplicitAutoJoints">true</Meta>`, `<string name="Name">Car</string>`, `<string name="Name">Wheel</string>`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{`class="Workspace"`, `<string name="Name">Other</string>`} {
		if strings.Contains(text, unwanted) {
			t.Errorf("shouldn't have %s in:\n%s", unwanted, text)
		}
	}
	if result.Instances != 3 {
		t.Errorf("Instances = %d, want 3", result.Instances)
	}

	// PrimaryPart points inside the model. The target of Link is outside the model.
	match := regexp.MustCompile(`<Ref name="PrimaryPart">(RBX[0-9A-F]+)</Ref>`).FindStringSubmatch(text)
	if match == nil || !strings.Contains(text, `<Item class="Part" referent="`+match[1]+`">`) {
		t.Errorf("PrimaryPart doesn't point at Wheel:\n%s", text)
	}
	if strings.Contains(text, `name="Value"`) {
		t.Errorf("wrote a Ref to something outside the model:\n%s", text)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "Workspace/Car/Link") {
		t.Errorf("warnings = %q, want one naming Workspace/Car/Link", result.Warnings)
	}
}

func TestPlacesHaveNoMeta(t *testing.T) {
	tr, _ := modelTree(t)
	if out, _ := build(t, tr); strings.Contains(out, "<Meta") {
		t.Errorf("Roblox doesn't write Meta for places:\n%s", out)
	}
}

func TestChoosingWhatAModelHolds(t *testing.T) {
	tr, mounts := modelTree(t)
	dir := t.TempDir()

	works := map[string]string{
		"Workspace/Car":       `<string name="Name">Car</string>`,
		"Workspace/Car/Wheel": `<string name="Name">Wheel</string>`,
		"Workspace/A/B":       `<string name="Name">A/B</string>`,
	}
	for instance, want := range works {
		path := filepath.Join(dir, "out.rbxmx")
		if _, err := WriteFile(path, tr, mounts, instance); err != nil {
			t.Errorf("%s: %v", instance, err)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s: missing %s in:\n%s", instance, want, data)
		}
	}

	fails := []struct{ output, instance, want string }{
		{"a.rbxmx", "", "needs an instance"},
		{"a.rbxmx", "Workspace/Nope", "nothing is at"},
		{"a.rbxmx", "Workspace/Twin", "2 instances"},
		{"a.rbxmx", "Workspace", "already exists in every game"},
		{"a.rbxlx", "Workspace/Car", "only works for .rbxmx"},
		{"a.rbxm", "Workspace/Car", "binary"},
	}
	for _, test := range fails {
		_, err := WriteFile(filepath.Join(dir, test.output), tr, mounts, test.instance)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s with %q: err = %v, want one with %q", test.output, test.instance, err, test.want)
		}
		if _, statErr := os.Stat(filepath.Join(dir, test.output)); statErr == nil {
			t.Errorf("%s with %q wrote a file", test.output, test.instance)
		}
	}
}

// sameValue compares wire values. Numbers can be different by the rounding of a 32-bit float or an 8-bit color.
// A ContentId can be a String on one side and Content on the other side.
func sameValue(want, got protocol.Value) error {
	if want.Type != got.Type && !(want.Type == "String" && got.Type == "Content") {
		return fmt.Errorf("got %s %s, want %s %s", got.Type, got.Value, want.Type, want.Value)
	}
	var a, b any
	if err := json.Unmarshal(want.Value, &a); err != nil {
		return err
	}
	if err := json.Unmarshal(got.Value, &b); err != nil {
		return err
	}
	if !closeEnough(a, b) {
		return fmt.Errorf("got %s, want %s", got.Value, want.Value)
	}
	return nil
}

func closeEnough(a, b any) bool {
	switch a := a.(type) {
	case float64:
		other, ok := b.(float64)
		return ok && math.Abs(a-other) < 1e-3
	case []any:
		other, ok := b.([]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for i := range a {
			if !closeEnough(a[i], other[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := b.(map[string]any)
		if !ok || len(a) != len(other) {
			return false
		}
		for key, value := range a {
			if !closeEnough(value, other[key]) {
				return false
			}
		}
		return true
	}
	return a == b
}

// TestBuildsAProjectFromDisk does the same steps as `verde build`.
func TestBuildsAProjectFromDisk(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"project.yml": "serve: {port: 1234}\nsync:\n  instances:\n    - instance: Workspace\n      path: src/ws\n    - instance: StarterPlayer/StarterPlayerScripts\n      path: src/client\n",
		"src/ws/Lamp.Part.inst.json": `{"props": {
"Anchored": {"t":"Bool","v":true},
"Size": {"t":"Vector3","v":[4,1,2]}
}}`,
		"src/client/Input.client.luau": "print('hi')\n",
	}
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Load(filepath.Join(dir, "project.yml"))
	if err != nil {
		t.Fatal(err)
	}
	tr, _, err := project.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out, warnings := build(t, tr)
	if len(warnings) > 0 {
		t.Errorf("warnings: %q", warnings)
	}
	for _, want := range []string{
		`<Item class="Workspace"`,
		`<Item class="Part"`,
		`<bool name="Anchored">true</bool>`,
		`<Vector3 name="size"><X>4</X><Y>1</Y><Z>2</Z></Vector3>`,
		`<Item class="StarterPlayer"`,
		`<Item class="StarterPlayerScripts"`,
		`<Item class="LocalScript"`,
		"<![CDATA[print('hi')\n]]>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `class="DataModel"`) {
		t.Errorf("the DataModel shouldn't be an Item:\n%s", out)
	}
}
