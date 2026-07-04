package resource

import (
	"fmt"
)

type Resource struct {
	Type      string
	Name      string
	dependsOn map[string]struct{}
}

func New(type_, name string) Resource {
	return Resource{
		Type:      type_,
		Name:      name,
		dependsOn: make(map[string]struct{}),
	}
}

func (r Resource) String() string {
	return r.ID()
}

func (r Resource) ID() string {
	return fmt.Sprintf("%s[%s]", r.Type, r.Name)
}
