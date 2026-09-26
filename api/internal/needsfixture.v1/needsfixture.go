// Package needsfixture is a test fixture for api's Needs(T.Method): its
// import path's last element contains a dot (like gopkg.in/yaml.v3), which
// the Go runtime escapes as %2e in function names while reflect's PkgPath
// does not.
package needsfixture

// Recipe is a RegisterMethods struct from a dotted package path.
type Recipe struct{}

// Base is the method another task needs by method expression.
func (Recipe) Base() {}

// Web is a second task method.
func (Recipe) Web() {}
