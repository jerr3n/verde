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

// reflection holds the data that the wire format does not give.
// For example, a Number can be a float, a double, or an int, and Size is saved as size.
// genreflection makes reflection.json from the rbx-dom database.
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
	// As is the saved name of the property. As is empty if the saved name is the same as the property name.
	As   string `json:"as"`
	Type string `json:"type"`
}

// database decodes reflection.json one time, on the first call.
var database = sync.OnceValue(func() *reflection {
	var db reflection
	if err := json.Unmarshal(reflectionJSON, &db); err != nil {
		// reflection.json is part of the executable, so an error here is a bug.
		panic("build: reflection.json: " + err.Error())
	}
	return &db
})

// savedAs returns the saved name and the type of a property. It also checks the superclasses of class.
// ok is false if Roblox does not save the property, or if the database does not know it.
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
