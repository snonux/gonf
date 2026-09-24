package api

import (
	"github.com/snonux/gonf/resource/configset"
	"github.com/snonux/gonf/resource/options"
)

// ConfigSetResource is the handle ConfigSet returns. It is itself a resource
// (for DependsOn/OnChange on the whole set, which changes when any member is
// published) and hands out per-member handles through Member and Members.
type ConfigSetResource = configset.Handle

// ConfigSet registers a validated multi-file configuration: its ConfigFile
// members are rendered once, staged together in a private directory,
// validated as a complete candidate set by every WithSetValidation command,
// and only then published, with bounded rollback if a publication fails
// part-way. Each member also gets a handle resource,
// ConfigSetMember[name/key], so a gate can watch one member:
//
//	mail := ConfigSet("smtpd",
//	    ConfigFile("aliases", "/etc/mail/aliases", WithSource(aliases)),
//	    ConfigFile("smtpd.conf", "/etc/mail/smtpd.conf", WithContent(conf)),
//	    WithSetValidation("smtpd", List("-n", "-f", MemberPath("smtpd.conf"))))
//	Command("newaliases", List(), OnChange(mail.Member("aliases")))
//	Service("smtpd", WithRestart, OnChange(mail.Member("smtpd.conf")))
//
// Member content refers to other members through MemberPath or
// MemberChrootPath, never through literal staged paths. See
// docs/design/config-set.md for the validation, locking and rollback guarantees.
func ConfigSet(name string, opts ...options.ConfigSetOption) ConfigSetResource {
	return configset.Present(name, opts...)
}
