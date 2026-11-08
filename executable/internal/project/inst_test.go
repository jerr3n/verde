// SPDX-License-Identifier: MPL-2.0

package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

func boolProp(name string, value bool) map[string]protocol.Value {
	encoded, _ := json.Marshal(value)
	return map[string]protocol.Value{name: {Type: "Bool", Value: encoded}}
}

func propsFile(lines ...string) string {
	return "{\n  \"props\": {\n    " + strings.Join(lines, ",\n    ") + "\n  }\n}\n"
}

// loads the project again, like a new server would. only what's on disk survives this
func (f *fixture) restart() map[string]protocol.Instance {
	f.t.Helper()
	next, _, err := Load(f.cfg)
	if err != nil {
		f.t.Fatalf("restart: %v", err)
	}
	return byPath(next)
}

func TestClassifyInst(t *testing.T) {
	tests := []struct {
		file, name, class string
		ok                bool
	}{
		{"Lamp.Part.inst.json", "Lamp", "Part", true},
		{"a.b.Part.inst.json", "a.b", "Part", true},
		{"init.Model.inst.json", "init", "Model", true},
		{"%69nit.Part.inst.json", "%69nit", "Part", true},
		{"Main.Script.inst.json", "Main", "Script", true},
		{"package.lock.json", "", "", false},
		{"config.json", "", "", false},
		{"Part.inst.json", "", "", false},
		{".Part.inst.json", "", "", false},
		{"Lamp.Pa-rt.inst.json", "", "", false},
		{"Lamp.9Part.inst.json", "", "", false},
		{"Lamp.Part.inst.json.tmp", "", "", false},
		{"Main.luau", "", "", false},
	}
	for _, tt := range tests {
		name, class, ok := classifyInst(tt.file)
		if name != tt.name || class != tt.class || ok != tt.ok {
			t.Errorf("classifyInst(%q) = %q, %q, %v", tt.file, name, class, ok)
		}
	}
}

func TestLoadNonScripts(t *testing.T) {
	cfg := newConfig(t, map[string]string{
		"s/Lamp.Part.inst.json":              `{"props": {"Anchored": {"t": "Bool", "v": true}, "Size": {"t": "Vector3", "v": [4, 1, 2]}}}`,
		"s/Empty.Part.inst.json":             `{}`,
		"s/Car/init.Model.inst.json":         `{"props": {"$tags": {"t": "Tags", "v": ["Fast"]}}}`,
		"s/Car/Wheel.Part.inst.json":         `{}`,
		"s/Car/Drive.server.luau":            "drive",
		"s/Main.server.luau":                 "main",
		"s/Main.Script.inst.json":            `{"props": {"Disabled": {"t": "Bool", "v": true}}}`,
		"s/Shop/init.luau":                   "shop",
		"s/Shop/init.ModuleScript.inst.json": `{"props": {"$tags": {"t": "Tags", "v": ["Store"]}}}`,
		"s/lib/init.Folder.inst.json":        `{"props": {"$tags": {"t": "Tags", "v": ["Code"]}}}`,
		"s/lib/Util.luau":                    "util",
		"s/package.lock.json":                `not read, because it isn't an instance`,
		"s/config.json":                      `not read either`,
	}, syncW)
	tr, _, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(tr)

	wantClass := map[string]string{
		"game/W/Lamp":      "Part",
		"game/W/Empty":     "Part",
		"game/W/Car":       "Model",
		"game/W/Car/Wheel": "Part",
		"game/W/Car/Drive": "Script",
		"game/W/Main":      "Script",
		"game/W/Shop":      "ModuleScript",
		"game/W/lib":       "Folder",
		"game/W/lib/Util":  "ModuleScript",
	}
	for full, class := range wantClass {
		if got[full].Class != class {
			t.Errorf("%s = %q, want %s", full, got[full].Class, class)
		}
	}
	// game, W, and everything above. nothing else should've been read as an instance
	if len(got) != len(wantClass)+2 {
		t.Errorf("instances = %v", sortedPaths(got))
	}

	if got["game/W/Lamp"].Props["Size"].Type != "Vector3" {
		t.Errorf("Lamp props = %+v", got["game/W/Lamp"].Props)
	}
	if got["game/W/Car"].Props["$tags"].Type != "Tags" {
		t.Errorf("Car props = %+v", got["game/W/Car"].Props)
	}
	if got["game/W/Main"].Props["Disabled"].Type != "Bool" || got["game/W/Main"].Props["Source"].Type != "String" {
		t.Errorf("Main props = %+v", got["game/W/Main"].Props)
	}
	if got["game/W/Shop"].Props["$tags"].Type != "Tags" || got["game/W/Shop"].Props["Source"].Type != "String" {
		t.Errorf("Shop props = %+v", got["game/W/Shop"].Props)
	}
	if got["game/W/lib"].Props["$tags"].Type != "Tags" {
		t.Errorf("lib props = %+v", got["game/W/lib"].Props)
	}
}

// a bad file stops the load instead of being skipped, since a snapshot missing an instance
// could make the plugin delete it
func TestLoadRejectsBadInstFiles(t *testing.T) {
	tests := map[string]map[string]string{
		"invalid JSON":      {"s/Lamp.Part.inst.json": `{`},
		"empty file":        {"s/Lamp.Part.inst.json": ``},
		"two values":        {"s/Lamp.Part.inst.json": `{} {}`},
		"unknown field":     {"s/Lamp.Part.inst.json": `{"prop": {}}`},
		"no type":           {"s/Lamp.Part.inst.json": `{"props": {"Size": {"v": [1, 2, 3]}}}`},
		"a Ref":             {"s/Lamp.Part.inst.json": `{"props": {"Other": {"t": "Ref", "v": "abc"}}}`},
		"invalid UTF-8":     {"s/Lamp.Part.inst.json": "{\"props\": {\"Name\": {\"t\": \"String\", \"v\": \"\xff\"}}}"},
		"Source":            {"s/Main.luau": "x", "s/Main.ModuleScript.inst.json": `{"props": {"Source": {"t": "String", "v": "y"}}}`},
		"file and folder":   {"s/Lamp.Part.inst.json": `{}`, "s/Lamp/init.Part.inst.json": `{}`},
		"two indexes":       {"s/Lamp/init.Part.inst.json": `{}`, "s/Lamp/init.Model.inst.json": `{}`},
		"script and class":  {"s/Lamp/init.luau": ``, "s/Lamp/init.Part.inst.json": `{}`},
		"init on a service": {"s/init.Part.inst.json": `{}`},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(newConfig(t, files, syncW)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestLoadIgnoresInstFiles(t *testing.T) {
	cfg := newConfig(t, map[string]string{
		"s/Hidden.Part.inst.json":       `{}`,
		"s/Secret.Part.inst.json":       `{}`,
		"s/Skip.Decal.inst.json":        `{}`,
		"s/Kept.Part.inst.json":         `{}`,
		"s/Main.luau":                   "x",
		"s/Main.ModuleScript.inst.json": `{"props": {"Bad": {"t": ""}}}`,
	}, syncW+"ignore: {paths: [\"**/Hidden.*\", \"**/Main.ModuleScript.inst.json\"], instances: [W/Secret], classes: [Decal]}\n")
	tr, _, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(tr)
	for _, full := range []string{"game/W/Hidden", "game/W/Secret", "game/W/Skip"} {
		if _, ok := got[full]; ok {
			t.Errorf("%s should be ignored", full)
		}
	}
	if _, ok := got["game/W/Kept"]; !ok {
		t.Error("Kept should load")
	}
	if _, ok := got["game/W/Main"].Props["Bad"]; ok {
		t.Error("an ignored .inst.json shouldn't be read")
	}
}

// the point of all this: a part from studio is still there after a restart
func TestPartsSurviveRestart(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")

	size := protocol.Value{Type: "Vector3", Value: json.RawMessage(`[4,1,2]`)}
	cframe := protocol.Value{Type: "CFrame", Value: json.RawMessage(`[0,5.5,0,1,0,0,0,1,0,0,0,1]`)}
	attrs := protocol.Value{Type: "Attributes", Value: json.RawMessage(`[["Speed",{"t":"Number","v":16}],["Note",{"t":"String","v":"a<b & c"}]]`)}
	tags := protocol.Value{Type: "Tags", Value: json.RawMessage(`["TagA","TagB"]`)}
	empty := protocol.Value{Type: "Nil"}

	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "car", Parent: w, Name: "Car", Class: "Model"},
		{ID: "wheel", Parent: "car", Name: "Wheel", Class: "Part", Props: map[string]protocol.Value{
			"Size": size, "CFrame": cframe, "$attributes": attrs, "$tags": tags, "MeshId": empty,
		}},
		script("drive", "wheel", "Drive", "Script", "print('vroom')"),
	}})

	got := f.restart()
	if got["game/W/Car"].Class != "Model" {
		t.Errorf("Car = %+v", got["game/W/Car"])
	}
	wheel := got["game/W/Car/Wheel"]
	if wheel.Class != "Part" {
		t.Errorf("Wheel = %+v", wheel)
	}
	for name, want := range map[string]protocol.Value{"Size": size, "CFrame": cframe, "$attributes": attrs, "$tags": tags, "MeshId": empty} {
		have, ok := wheel.Props[name]
		if !ok || have.Type != want.Type || string(have.Value) != string(want.Value) {
			t.Errorf("%s = %+v, want %+v", name, have, want)
		}
	}
	if got["game/W/Car/Wheel/Drive"].Class != "Script" {
		t.Errorf("Drive = %+v", got["game/W/Car/Wheel/Drive"])
	}
}

func TestApplyUpdatesPartFile(t *testing.T) {
	f := newFixture(t, nil, syncW)
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "lamp", Parent: f.ref("game/W"), Name: "Lamp", Class: "Part", Props: boolProp("Anchored", true)},
	}})
	f.assertFile("s/Lamp.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":true}`))

	// an update merges, so the file has to hold both, sorted so diffs stay small
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lamp", Props: boolProp("CanCollide", false)}}})
	f.assertFile("s/Lamp.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":true}`, `"CanCollide": {"t":"Bool","v":false}`))

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lamp", Props: boolProp("Anchored", false)}}})
	f.assertFile("s/Lamp.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":false}`, `"CanCollide": {"t":"Bool","v":false}`))

	// a prop cleared in studio has to stay in the file, or a restart would lose it
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lamp", Props: map[string]protocol.Value{"CanCollide": {Type: "Nil"}}}}})
	f.assertFile("s/Lamp.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":false}`, `"CanCollide": {"t":"Nil"}`))
}

func TestApplyRenamesAndMovesParts(t *testing.T) {
	f := newFixture(t, map[string]string{"s/lib/.keep": ""}, syncW)
	w, lib := f.ref("game/W"), f.ref("game/W/lib")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "lamp", Parent: w, Name: "Lamp", Class: "Part", Props: boolProp("Anchored", true)},
	}})
	want := propsFile(`"Anchored": {"t":"Bool","v":true}`)

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lamp", Name: "Light"}}})
	f.assertFile("s/Light.Part.inst.json", want)
	f.assertMissing("s/Lamp.Part.inst.json")

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lamp", Parent: lib}}})
	f.assertFile("s/lib/Light.Part.inst.json", want)
	f.assertMissing("s/Light.Part.inst.json")

	// with children it's a folder, which moves whole
	f.apply(protocol.Patch{Added: []protocol.Instance{script("glow", "lamp", "Glow", "Script", "glow")}})
	f.assertFile("s/lib/Light/init.Part.inst.json", want)
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lamp", Parent: w, Name: "Lamp"}}})
	f.assertFile("s/Lamp/init.Part.inst.json", want)
	f.assertFile("s/Lamp/Glow.server.luau", "glow")
	f.assertMissing("s/lib/Light")
}

func TestApplyPartGetsChildren(t *testing.T) {
	f := newFixture(t, nil, syncW)
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "lamp", Parent: f.ref("game/W"), Name: "Lamp", Class: "Part", Props: boolProp("Anchored", true)},
	}})
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "light", Parent: "lamp", Name: "Glow", Class: "PointLight", Props: boolProp("Enabled", false)},
	}})
	f.assertMissing("s/Lamp.Part.inst.json")
	f.assertFile("s/Lamp/init.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":true}`))
	f.assertFile("s/Lamp/Glow.PointLight.inst.json", propsFile(`"Enabled": {"t":"Bool","v":false}`))
}

func TestApplyRemovesPartsToTrash(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "lamp", Parent: w, Name: "Lamp", Class: "Part", Props: boolProp("Anchored", true)},
		{ID: "car", Parent: w, Name: "Car", Class: "Model"},
		{ID: "wheel", Parent: "car", Name: "Wheel", Class: "Part"},
	}})

	f.apply(protocol.Patch{Removed: []protocol.Ref{"lamp", "car", "wheel"}})
	if got := f.files(); len(got) != 0 {
		t.Errorf("files = %v", got)
	}
	if got := f.trashed("s/Lamp.Part.inst.json"); !slices.Equal(got, []string{propsFile(`"Anchored": {"t":"Bool","v":true}`)}) {
		t.Errorf("trash = %v", got)
	}
	if got := f.trashed("s/Car/Wheel.Part.inst.json"); len(got) != 1 {
		t.Errorf("trash = %v", got)
	}
}

func TestPropsFileFollowsItsScript(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "x", "s/lib/.keep": ""}, syncW)
	id, lib := f.ref("game/W/Main"), f.ref("game/W/lib")
	want := propsFile(`"Disabled": {"t":"Bool","v":true}`)
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: id, Props: boolProp("Disabled", true)}}})

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: id, Name: "Boot"}}})
	f.assertFile("s/Boot.server.luau", "x")
	f.assertFile("s/Boot.Script.inst.json", want)
	f.assertMissing("s/Main.Script.inst.json")

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: id, Parent: lib}}})
	f.assertFile("s/lib/Boot.Script.inst.json", want)
	f.assertMissing("s/Boot.Script.inst.json")

	// getting a child turns it into a folder, and the file goes inside
	f.apply(protocol.Patch{Added: []protocol.Instance{script("kid", id, "Kid", "ModuleScript", "kid")}})
	f.assertFile("s/lib/Boot/init.server.luau", "x")
	f.assertFile("s/lib/Boot/init.Script.inst.json", want)
	f.assertMissing("s/lib/Boot.Script.inst.json")

	got := f.restart()
	if got["game/W/lib/Boot"].Props["Disabled"].Type != "Bool" {
		t.Errorf("Boot = %+v", got["game/W/lib/Boot"])
	}

	f.apply(protocol.Patch{Removed: []protocol.Ref{id}})
	f.assertMissing("s/lib/Boot")
}

func TestScriptDeleteTrashesItsPropsFile(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "x"}, syncW)
	id := f.ref("game/W/Main")
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: id, Props: boolProp("Disabled", true)}}})
	f.apply(protocol.Patch{Removed: []protocol.Ref{id}})
	if got := f.files(); len(got) != 0 {
		t.Errorf("files = %v", got)
	}
	if got := f.trashed("s/Main.Script.inst.json"); len(got) != 1 {
		t.Errorf("trash = %v", got)
	}
}

// deleting a script's file outside verde should delete the script, even if its .inst.json
// is still there. otherwise it'd come back with no text
func TestLeftoverPropsFileDoesntBringBackScript(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/Main.server.luau":      "x",
		"s/Main.Script.inst.json": `{"props": {"Disabled": {"t": "Bool", "v": true}}}`,
	}, syncW)
	id := f.ref("game/W/Main")

	os.Remove(filepath.Join(f.cfg.Root, "s/Main.server.luau"))
	next, _, err := Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if patch := tree.Diff(f.tree, next); !slices.Equal(patch.Removed, []protocol.Ref{id}) {
		t.Errorf("patch = %+v", patch)
	}
}

// a leftover .inst.json holds values the tree doesn't have, and a scan would send them to
// studio. the new script wins, and the leftover goes to the trash
func TestNewScriptReplacesLeftoverPropsFile(t *testing.T) {
	leftover := `{"props": {"Disabled": {"t": "Bool", "v": true}}}`
	f := newFixture(t, map[string]string{
		"s/Old.server.luau":      "old",
		"s/New.Script.inst.json": leftover,
	}, syncW)
	f.apply(protocol.Patch{Added: []protocol.Instance{script("n", f.ref("game/W"), "New", "Script", "new")}})
	f.assertMissing("s/New.Script.inst.json")

	// renaming onto one has the same problem
	os.WriteFile(filepath.Join(f.cfg.Root, "s/Next.Script.inst.json"), []byte(leftover), 0o644)
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/Old"), Name: "Next"}}})
	f.assertMissing("s/Next.Script.inst.json")
}

func TestFolderProps(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	tags := map[string]protocol.Value{"$tags": {Type: "Tags", Value: json.RawMessage(`["Code"]`)}}
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "plain", Parent: w, Name: "Plain", Class: "Folder"},
		{ID: "lib", Parent: w, Name: "lib", Class: "Folder", Props: tags},
	}})

	// only a folder with something to store has a file, to keep the source tree clean
	f.assertMissing("s/Plain/init.Folder.inst.json")
	f.assertFile("s/lib/init.Folder.inst.json", propsFile(`"$tags": {"t":"Tags","v":["Code"]}`))

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "plain", Props: tags}}})
	f.assertFile("s/Plain/init.Folder.inst.json", propsFile(`"$tags": {"t":"Tags","v":["Code"]}`))

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "lib", Name: "code"}}})
	f.assertFile("s/code/init.Folder.inst.json", propsFile(`"$tags": {"t":"Tags","v":["Code"]}`))
	if f.restart()["game/W/code"].Class != "Folder" {
		t.Error("code should still be a Folder")
	}
}

func TestRefPropsStayOutOfFiles(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "base", Parent: w, Name: "Base", Class: "Part"},
		{ID: "car", Parent: w, Name: "Car", Class: "Model", Props: map[string]protocol.Value{
			"PrimaryPart": {Type: "Ref", Value: json.RawMessage(`"base"`)},
		}},
	}})
	f.assertFile("s/Car.Model.inst.json", "{}\n")
	if inst, _ := f.tree.Get("car"); inst.Props["PrimaryPart"].Type != "Ref" {
		t.Errorf("the Ref should stay in the tree, have %+v", inst.Props)
	}
}

// service props aren't synced, so there's no file for them, and an update mustn't make one
func TestMountPropsStayOutOfFiles(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: w, Props: boolProp("Enabled", true)}}})
	if got := f.files(); len(got) != 0 {
		t.Errorf("files = %v", got)
	}
	if f.tree.IsFileless(w) {
		t.Error("the mount shouldn't be fileless")
	}
}

func TestApplyDangerousNamesAndClasses(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")

	names := []string{"../../escape", "init", "NUL", ".git", "a.Part.inst.json", "x.server", "tricky."}
	var added []protocol.Instance
	for _, name := range names {
		added = append(added, protocol.Instance{ID: protocol.Ref("id-" + name), Parent: w, Name: name, Class: "Part"})
	}
	f.apply(protocol.Patch{Added: added})

	got := f.restart()
	for _, name := range names {
		if got["game/W/"+name].Class != "Part" {
			t.Errorf("%q didn't round trip, have %v", name, sortedPaths(got))
		}
	}

	// a class is part of the file name, and it comes from the client too
	for _, class := range []string{"../../escape", "a/b", "a.b", "", "init.luau"} {
		_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
			{ID: protocol.Ref("bad-" + class), Parent: w, Name: "Bad", Class: class},
		}})
		if len(errs) != 1 {
			t.Errorf("class %q: errs = %v, want one", class, errs)
		}
		if !f.tree.IsFileless(protocol.Ref("bad-" + class)) {
			t.Errorf("class %q should be fileless", class)
		}
	}
	f.assertNoEcho()
	if escaped, _ := filepath.Glob(filepath.Join(filepath.Dir(f.cfg.Root), "escape*")); len(escaped) != 0 {
		t.Errorf("wrote outside the project: %v", escaped)
	}
}

func TestSameNameDifferentClassParts(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "part", Parent: w, Name: "Lamp", Class: "Part"},
		{ID: "model", Parent: w, Name: "Lamp", Class: "Model"},
		script("mod", w, "Lamp", "ModuleScript", "x"),
	}})
	f.assertFile("s/Lamp.Part.inst.json", "{}\n")
	f.assertFile("s/Lamp.Model.inst.json", "{}\n")
	f.assertFile("s/Lamp.luau", "x")
}

// studio is full of parts called Part, and each needs its own file
func TestSameNameParts(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "first", Parent: w, Name: "Part", Class: "Part", Props: boolProp("Anchored", true)},
		{ID: "second", Parent: w, Name: "Part", Class: "Part", Props: boolProp("Anchored", false)},
		{ID: "third", Parent: w, Name: "Part", Class: "Part", Props: boolProp("CanCollide", false)},
	}})

	first, second := "s/Part.Part.inst.json", "s/Part"+tagFor("second")+".Part.inst.json"
	f.assertFile(first, "{\n  \"id\": \"first\",\n  \"props\": {\n    \"Anchored\": {\"t\":\"Bool\",\"v\":true}\n  }\n}\n")
	f.assertFile(second, "{\n  \"id\": \"second\",\n  \"name\": \"Part\",\n  \"props\": {\n    \"Anchored\": {\"t\":\"Bool\",\"v\":false}\n  }\n}\n")
	if got := f.files(); len(got) != 3 {
		t.Errorf("files = %v", got)
	}

	// a change goes to the right file, and comes back with the right instance
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "second", Props: boolProp("Locked", true)}}})
	f.assertFile(first, "{\n  \"id\": \"first\",\n  \"props\": {\n    \"Anchored\": {\"t\":\"Bool\",\"v\":true}\n  }\n}\n")
	f.assertFile(second, "{\n  \"id\": \"second\",\n  \"name\": \"Part\",\n  \"props\": {\n    \"Anchored\": {\"t\":\"Bool\",\"v\":false},\n    \"Locked\": {\"t\":\"Bool\",\"v\":true}\n  }\n}\n")

	next, _, err := Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []protocol.Ref{"first", "second", "third"} {
		inst, ok := next.Get(id)
		if !ok || inst.Name != "Part" || inst.Class != "Part" {
			t.Errorf("%s = %+v, %v", id, inst, ok)
		}
	}
	if inst, _ := next.Get("second"); inst.Props["Locked"].Type != "Bool" {
		t.Errorf("second = %+v", inst)
	}
}

// nothing here should send a patch back, or lose track of which file is whose
func TestSameNamePartsLifecycle(t *testing.T) {
	f := newFixture(t, map[string]string{"s/lib/.keep": ""}, syncW)
	w, lib := f.ref("game/W"), f.ref("game/W/lib")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "first", Parent: w, Name: "Part", Class: "Part"},
		{ID: "second", Parent: w, Name: "Part", Class: "Part", Props: boolProp("Anchored", true)},
	}})
	second := "s/Part" + tagFor("second")

	// getting a child: the tag stays with it
	f.apply(protocol.Patch{Added: []protocol.Instance{{ID: "kid", Parent: "second", Name: "Glow", Class: "PointLight"}}})
	f.assertMissing(second + ".Part.inst.json")
	f.assertFile(second+"/Glow.PointLight.inst.json", "{}\n")
	if !strings.Contains(f.read(second+"/init.Part.inst.json"), `"name": "Part"`) {
		t.Errorf("no name in %s", f.read(second+"/init.Part.inst.json"))
	}

	// the first, with children too, needs a folder with the same name. it's the first's
	f.apply(protocol.Patch{Added: []protocol.Instance{{ID: "kid2", Parent: "first", Name: "Glow", Class: "PointLight"}}})
	f.assertFile("s/Part/Glow.PointLight.inst.json", "{}\n")

	// a rename to a free name loses the tag, and the name and id it needed
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "second", Name: "Lamp"}}})
	f.assertFile("s/Lamp/init.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":true}`))
	f.assertMissing(second)

	// a rename onto a taken name gets one
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "second", Name: "Part"}}})
	f.assertMissing("s/Lamp")
	if !strings.Contains(f.read(second+"/init.Part.inst.json"), `"id": "second"`) {
		t.Errorf("no id in %s", f.read(second+"/init.Part.inst.json"))
	}

	// a move to where it's the only one loses it too
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "second", Parent: lib}}})
	f.assertFile("s/lib/Part/init.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":true}`))
	f.assertMissing(second)

	f.apply(protocol.Patch{Removed: []protocol.Ref{"first", "kid2"}})
	f.assertMissing("s/Part")
	f.assertFile("s/lib/Part/Glow.PointLight.inst.json", "{}\n")
}

func (f *fixture) read(name string) string {
	f.t.Helper()
	text, err := os.ReadFile(filepath.Join(f.cfg.Root, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(text)
}

func TestSameNameEachClass(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "f1", Parent: w, Name: "lib", Class: "Folder"},
		{ID: "f2", Parent: w, Name: "lib", Class: "Folder", Props: boolProp("Enabled", true)},
		script("k1", "f1", "Kid", "ModuleScript", "one"),
		script("k2", "f2", "Kid", "ModuleScript", "two"),
		script("s1", w, "Run", "Script", "a"),
		script("s2", w, "Run", "Script", "b"),
		{ID: "m1", Parent: w, Name: "Car", Class: "Model"},
		{ID: "m2", Parent: w, Name: "Car", Class: "Model"},
		script("d1", "m1", "Drive", "Script", "x"),
		script("d2", "m2", "Drive", "Script", "y"),
	}})

	f.assertFile("s/lib/Kid.luau", "one")
	f.assertFile("s/lib"+tagFor("f2")+"/Kid.luau", "two")
	f.assertFile("s/Run.server.luau", "a")
	f.assertFile("s/Run"+tagFor("s2")+".server.luau", "b")
	f.assertFile("s/Car/Drive.server.luau", "x")
	f.assertFile("s/Car"+tagFor("m2")+"/Drive.server.luau", "y")

	next, _, err := Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []protocol.Ref{"f2", "s2", "m2", "f1", "s1", "m1"} {
		if _, ok := next.Get(id); !ok {
			t.Errorf("%s should keep its id", id)
		}
	}
}

// a sibling added in the same patch that frees the name doesn't need a tag
func TestTagOnlyLastsAsLongAsNeeded(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{{ID: "old", Parent: w, Name: "Part", Class: "Part"}}})
	f.apply(protocol.Patch{
		Added:   []protocol.Instance{{ID: "new", Parent: w, Name: "Part", Class: "Part", Props: boolProp("Anchored", true)}},
		Removed: []protocol.Ref{"old"},
	})
	if got := f.files(); !slices.Equal(got, []string{"s/Part.Part.inst.json"}) {
		t.Errorf("files = %v", got)
	}
	f.assertFile("s/Part.Part.inst.json", propsFile(`"Anchored": {"t":"Bool","v":true}`))
}

// a lone instance has a file with no id, until a second one turns up
func TestLoneInstanceHasNoId(t *testing.T) {
	f := newFixture(t, nil, syncW)
	f.apply(protocol.Patch{Added: []protocol.Instance{{ID: "a", Parent: f.ref("game/W"), Name: "Part", Class: "Part"}}})
	f.assertFile("s/Part.Part.inst.json", "{}\n")
}

// someone renames a tagged file to something readable. it holds its name and id, so it's
// still the same instance, and changes have to find it instead of making a duplicate
func TestRenamedTaggedFileIsStillFound(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "first", Parent: w, Name: "Part", Class: "Part"},
		{ID: "second", Parent: w, Name: "Part", Class: "Part"},
	}})
	root := f.cfg.Root
	if err := os.Rename(filepath.Join(root, "s/Part"+tagFor("second")+".Part.inst.json"), filepath.Join(root, "s/Wheel.Part.inst.json")); err != nil {
		t.Fatal(err)
	}
	f.assertNoEcho()

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "second", Props: boolProp("Anchored", true)}}})
	if got := f.files(); len(got) != 2 {
		t.Errorf("files = %v", got)
	}
	if !strings.Contains(f.read("s/Wheel.Part.inst.json"), `"Anchored"`) {
		t.Errorf("the change didn't reach its file: %s", f.read("s/Wheel.Part.inst.json"))
	}
	f.assertFile("s/Part.Part.inst.json", "{\n  \"id\": \"first\"\n}\n")
}

func TestLoadRejectsAmbiguousFiles(t *testing.T) {
	tests := map[string]map[string]string{
		"a copied id": {
			"s/A.Part.inst.json": `{"id": "same"}`,
			"s/B.Part.inst.json": `{"id": "same"}`,
		},
		"two with no id": {
			"s/A.Part.inst.json": `{"name": "Wheel"}`,
			"s/B.Part.inst.json": `{"name": "Wheel"}`,
		},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(newConfig(t, files, syncW)); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestLoadUsesStoredNameAndId(t *testing.T) {
	tr, _, err := Load(newConfig(t, map[string]string{
		"s/A.Part.inst.json":         `{"id": "one", "name": "Wheel"}`,
		"s/B.Part.inst.json":         `{"id": "two", "name": "Wheel"}`,
		"s/C.Part.inst.json":         `{"name": "Wheel"}`,
		"s/D/init.Model.inst.json":   `{"id": "three", "name": "Car"}`,
		"s/E.luau":                   "x",
		"s/E.ModuleScript.inst.json": `{"id": "four", "name": "Util"}`,
		"s/F/init.server.luau":       "y",
		"s/F/init.Script.inst.json":  `{"id": "five", "name": "Run"}`,
		"s/G/init.Folder.inst.json":  `{"id": "six", "name": "lib"}`,
		"s/H/init.Folder.inst.json":  `{}`,
		"s/H/Kid.Part.inst.json":     `{"id": "seven"}`,
	}, syncW))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[protocol.Ref][2]string{
		"one": {"Wheel", "Part"}, "two": {"Wheel", "Part"},
		"three": {"Car", "Model"}, "four": {"Util", "ModuleScript"}, "five": {"Run", "Script"},
		"six": {"lib", "Folder"}, "seven": {"Kid", "Part"},
	} {
		inst, ok := tr.Get(id)
		if !ok || inst.Name != want[0] || inst.Class != want[1] {
			t.Errorf("%s = %+v, %v, want %v", id, inst, ok, want)
		}
	}
}

// files moved by hand keep their ids, so the instance moves instead of being replaced
func TestMovingFileByHandMovesTheInstance(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/old/Wheel.Part.inst.json": `{"id": "w", "props": {"Anchored": {"t": "Bool", "v": true}}}`,
		"s/lib/.keep":                "",
	}, syncW)
	lib := f.ref("game/W/lib")
	root := f.cfg.Root
	if err := os.Rename(filepath.Join(root, "s/old/Wheel.Part.inst.json"), filepath.Join(root, "s/lib/Tire.Part.inst.json")); err != nil {
		t.Fatal(err)
	}

	next, _, err := Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	patch := tree.Diff(f.tree, next)
	if len(patch.Added)+len(patch.Removed) != 0 || len(patch.Updated) != 1 {
		t.Fatalf("patch = %+v", patch)
	}
	if u := patch.Updated[0]; u.ID != "w" || u.Parent != lib || u.Name != "Tire" {
		t.Errorf("update = %+v", u)
	}
	if err := f.tree.Apply(patch); err != nil {
		t.Fatal(err)
	}
	f.assertNoEcho()
}

func TestApplyIgnoredInstFiles(t *testing.T) {
	f := newFixture(t, nil, syncW+"ignore: {paths: [\"**/*.inst.json\"]}\n")
	w := f.ref("game/W")

	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		{ID: "lamp", Parent: w, Name: "Lamp", Class: "Part"},
		{ID: "main", Parent: w, Name: "Main", Class: "Script", Props: mergeProps(srcProps("x"), boolProp("Disabled", true))},
		{ID: "lib", Parent: w, Name: "lib", Class: "Folder", Props: boolProp("Enabled", true)},
	}})
	// a part has nowhere else to be. a script and a folder still exist without the file
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	if !f.tree.IsFileless("lamp") {
		t.Error("lamp should be fileless")
	}
	if f.tree.IsFileless("main") || f.tree.IsFileless("lib") {
		t.Error("main and lib should still have their files")
	}
	f.assertFile("s/Main.server.luau", "x")
	f.assertMissing("s/Main.Script.inst.json")
	f.assertMissing("s/lib/init.Folder.inst.json")
	f.assertNoEcho()
}

// a second instance needs a .inst.json to hold its name. if that's ignored it can't have
// one, and the first's file has to stay untouched
func TestSameNameWithIgnoredInstFiles(t *testing.T) {
	f := newFixture(t, nil, syncW+"ignore: {paths: [\"**/*.inst.json\"]}\n")
	w := f.ref("game/W")
	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		script("first", w, "Dup", "ModuleScript", "first"),
		script("second", w, "Dup", "ModuleScript", "second"),
	}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	f.assertFile("s/Dup.luau", "first")
	if got := f.files(); len(got) != 1 {
		t.Errorf("files = %v", got)
	}
	if !f.tree.IsFileless("second") {
		t.Error("second should be fileless")
	}
	f.assertNoEcho()
}

func mergeProps(a, b map[string]protocol.Value) map[string]protocol.Value {
	out := map[string]protocol.Value{}
	for name, value := range a {
		out[name] = value
	}
	for name, value := range b {
		out[name] = value
	}
	return out
}

// saving the same state twice has to leave the bytes alone, or every prop change would
// rewrite unrelated files, and git would show diffs that mean nothing
func TestEncodeInstIsStable(t *testing.T) {
	props := map[string]protocol.Value{
		"Zed":     {Type: "Number", Value: json.RawMessage(`1`)},
		"Alpha":   {Type: "String", Value: json.RawMessage(`"a<b>&c"`)},
		"Spaces":  {Type: "Vector3", Value: json.RawMessage(`[1, 2, 3]`)},
		"Nothing": {Type: "Nil"},
	}
	want := propsFile(
		`"Alpha": {"t":"String","v":"a<b>&c"}`,
		`"Nothing": {"t":"Nil"}`,
		`"Spaces": {"t":"Vector3","v":[1,2,3]}`,
		`"Zed": {"t":"Number","v":1}`,
	)
	for range 5 {
		got, err := encodeInst(instFile{Props: props})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got:\n%s\nwant:\n%s", got, want)
		}
	}

	back, err := decodeInst([]byte(want), "Part")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Props["Alpha"], protocol.Value{Type: "String", Value: json.RawMessage(`"a<b>&c"`)}) {
		t.Errorf("Alpha = %+v", back.Props["Alpha"])
	}
	if text, _ := encodeInst(back); text != want {
		t.Errorf("reencoding changed the file:\n%s", text)
	}
}

func TestEncodeInstRejectsInvalidText(t *testing.T) {
	_, err := encodeInst(instFile{Props: map[string]protocol.Value{"Bad": {Type: "String", Value: json.RawMessage("\"\xff\"")}}})
	if err == nil {
		t.Error("expected an error, because Load would refuse this file")
	}
}

func TestDiffSeesHandEditedInstFiles(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/Lamp.Part.inst.json": `{"props": {"Anchored": {"t": "Bool", "v": true}, "Size": {"t": "Vector3", "v": [1, 2, 3]}}}`,
		"s/Old.Part.inst.json":  `{}`,
	}, syncW)
	lamp, old := f.ref("game/W/Lamp"), f.ref("game/W/Old")
	root := f.cfg.Root

	// reformatting isn't a change
	os.WriteFile(filepath.Join(root, "s/Lamp.Part.inst.json"), []byte(`{"props":{"Size":{"t":"Vector3","v":[1.0,2,3]},"Anchored":{"v":true,"t":"Bool"}}}`), 0o644)
	f.assertNoEcho()

	os.WriteFile(filepath.Join(root, "s/Lamp.Part.inst.json"), []byte(`{"props":{"Size":{"t":"Vector3","v":[9,2,3]},"Anchored":{"t":"Bool","v":true}}}`), 0o644)
	os.Remove(filepath.Join(root, "s/Old.Part.inst.json"))
	os.WriteFile(filepath.Join(root, "s/New.Decal.inst.json"), []byte(`{"props":{"Transparency":{"t":"Number","v":0.5}}}`), 0o644)

	next, _, err := Load(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	patch := tree.Diff(f.tree, next)

	if len(patch.Updated) != 1 || patch.Updated[0].ID != lamp || string(patch.Updated[0].Props["Size"].Value) != `[9,2,3]` || len(patch.Updated[0].Props) != 1 {
		t.Errorf("updated = %+v", patch.Updated)
	}
	if !slices.Equal(patch.Removed, []protocol.Ref{old}) {
		t.Errorf("removed = %+v", patch.Removed)
	}
	if len(patch.Added) != 1 || patch.Added[0].Name != "New" || patch.Added[0].Class != "Decal" || patch.Added[0].Props["Transparency"].Type != "Number" {
		t.Errorf("added = %+v", patch.Added)
	}
}
