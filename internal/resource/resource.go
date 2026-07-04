package resource

import (
	"fmt"
)

type Resource struct {
	Type      string
	Name      string
	dependsOn map[string]struct{}
}

func Register(type_, name string) Resource {
	r := Resource{
		Type:      type_,
		Name:      name,
		dependsOn: make(map[string]struct{}),
	}

	if err := getRepository().register(r); err != nil {
		panic(err)
	}

	return r
}

func (r Resource) String() string {
	return r.ID()
}

func (r Resource) ID() string {
	return fmt.Sprintf("%s[%s]", r.Type, r.Name)
}
