package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

type Ref string

func NewRef() Ref {
	return Ref(uuid.NewString())
}

type Value struct {
	Type  string          `json:"t"`
	Value json.RawMessage `json:"v"`
}

func StringValue(s string) Value {
	b, _ := json.Marshal(s) // marshaling a string cannot fail
	return Value{Type: "String", Value: b}
}

type Instance struct {
	ID         Ref              `json:"id"`
	Parent     Ref              `json:"parent,omitempty"`
	Name       string           `json:"name"`
	ClassName  string           `json:"class"`
	Properties map[string]Value `json:"props,omitempty"`

	Children []Ref `json:"-"`

	Path string `json:"-"`
}

var (
	ErrNotFound      = errors.New("instance not found")
	ErrDuplicateRef  = errors.New("ref already exists")
	ErrRootImmutable = errors.New("cannot remove or reparent the root")
	ErrCycle         = errors.New("cannot parent an instance to itself or its descendant")
)

type Tree struct {
	root      Ref
	instances map[Ref]*Instance
}

func NewTree(name, className string) *Tree {
	root := &Instance{
		ID:         NewRef(),
		Name:       name,
		ClassName:  className,
		Properties: map[string]Value{},
	}
	return &Tree{
		root:      root.ID,
		instances: map[Ref]*Instance{root.ID: root},
	}
}

func (t *Tree) Root() Ref { return t.root }

func (t *Tree) Len() int { return len(t.instances) }

func (t *Tree) Get(id Ref) (*Instance, bool) {
	inst, ok := t.instances[id]
	return inst, ok
}

func (t *Tree) Insert(parent Ref, inst Instance) (Ref, error) {
	p, ok := t.instances[parent]
	if !ok {
		return "", fmt.Errorf("insert under %s: parent: %w", parent, ErrNotFound)
	}
	if inst.ID == "" {
		inst.ID = NewRef()
	} else if _, exists := t.instances[inst.ID]; exists {
		return "", fmt.Errorf("insert %s: %w", inst.ID, ErrDuplicateRef)
	}
	if inst.Properties == nil {
		inst.Properties = map[string]Value{}
	}
	inst.Parent = parent
	inst.Children = nil

	t.instances[inst.ID] = &inst
	p.Children = append(p.Children, inst.ID)
	return inst.ID, nil
}

func (t *Tree) Remove(id Ref) error {
	if id == t.root {
		return ErrRootImmutable
	}
	inst, ok := t.instances[id]
	if !ok {
		return fmt.Errorf("remove %s: %w", id, ErrNotFound)
	}
	if p, ok := t.instances[inst.Parent]; ok {
		p.Children = removeRef(p.Children, id)
	}
	t.Walk(id, func(d *Instance) bool {
		delete(t.instances, d.ID)
		return true
	})
	return nil
}

func (t *Tree) SetParent(id, newParent Ref) error {
	if id == t.root {
		return ErrRootImmutable
	}
	inst, ok := t.instances[id]
	if !ok {
		return fmt.Errorf("reparent %s: %w", id, ErrNotFound)
	}
	np, ok := t.instances[newParent]
	if !ok {
		return fmt.Errorf("reparent %s under %s: parent: %w", id, newParent, ErrNotFound)
	}
	if inst.Parent == newParent {
		return nil
	}
	for a := newParent; a != ""; a = t.instances[a].Parent {
		if a == id {
			return fmt.Errorf("reparent %s under %s: %w", id, newParent, ErrCycle)
		}
	}

	if op, ok := t.instances[inst.Parent]; ok {
		op.Children = removeRef(op.Children, id)
	}
	inst.Parent = newParent
	np.Children = append(np.Children, id)
	return nil
}

func (t *Tree) Walk(id Ref, fn func(*Instance) bool) {
	inst, ok := t.instances[id]
	if !ok {
		return
	}
	children := inst.Children
	if !fn(inst) {
		return
	}
	for _, c := range children {
		t.Walk(c, fn)
	}
}

func (t *Tree) Flatten(id Ref) []*Instance {
	var out []*Instance
	t.Walk(id, func(i *Instance) bool {
		out = append(out, i)
		return true
	})
	return out
}

func removeRef(refs []Ref, id Ref) []Ref {
	return slices.DeleteFunc(refs, func(r Ref) bool { return r == id })
}
