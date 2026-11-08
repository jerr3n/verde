// SPDX-License-Identifier: MPL-2.0

package build

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:generate go run ./genreflection

//go:embed reflection.json
var reflectionJSON []byte

// reflection says what the wire format doesn't: a Number could be a float, double or int,
// and Size is saved as size.
type reflection struct {
	Version string                      `json:"version"`
	Classes map[string]reflectedClass   `json:"classes"`
	Enums   map[string]map[string]int64 `json:"enums"`
}

type reflectedClass struct {
	Super string                   `json:"super"`
	Props map[string]reflectedProp `json:"props"`
}

type reflectedProp struct {
	As   string `json:"as"` // the name it's saved under, if that's not its own
	Type string `json:"type"`
}

var database = sync.OnceValue(func() *reflection {
	var db reflection
	if err := json.Unmarshal(reflectionJSON, &db); err != nil {
		panic("build: reflection.json: " + err.Error()) // it's part of the executable
	}
	return &db
})

// savedAs returns the name and type a property is written with. ok is false for a property
// Roblox doesn't save, or one the database doesn't know.
func (r *reflection) savedAs(class, name string) (saved, dataType string, ok bool) {
	for class != "" {
		c, known := r.Classes[class]
		if !known {
			return "", "", false
		}
		if p, found := c.Props[name]; found {
			if p.As != "" {
				return p.As, p.Type, true
			}
			return name, p.Type, true
		}
		class = c.Super
	}
	return "", "", false
}
