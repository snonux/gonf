package frontend

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
)

// httpdConf is the main httpd configuration. It includes gonfy.conf by
// MemberPath, so the validator parses the staged copy of it.
var httpdConf = `# Managed by gonf. Gonfy owns the front door of this lodge.
ServerRoot "/etc/httpd"
Listen 80
Include conf.modules.d/*.conf
User apache
Group apache
ServerName localhost
ServerAdmin root@localhost
ErrorLog "logs/error_log"
LogLevel warn
LogFormat "%h %l %u %t \"%r\" %>s %b \"%{Referer}i\" \"%{User-Agent}i\"" combined
CustomLog "logs/access_log" combined
TypesConfig /etc/mime.types
AddDefaultCharset UTF-8
<Directory />
    AllowOverride None
    Require all denied
</Directory>
Include "` + MemberPath("gonfy.conf") + `"
`
