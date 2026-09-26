#!/bin/sh
# Install Go and what the live BSD package/service tests expect (rsync;
# uptimed, or bozohttpd from base on NetBSD). Packages the VM image already
# has are left alone: upgrading them can fail on a dependency skew.
set -eu
case "$(uname -s)" in
FreeBSD)
	for p in go git rsync uptimed; do
		pkg info -e "$p" || pkg install -y "$p"
	done
	;;
OpenBSD)
	for p in go git rsync uptimed; do
		pkg_info -e "$p-*" >/dev/null || pkg_add "$p" || pkg_add "$p--"
	done
	;;
NetBSD)
	for p in go git rsync; do
		/usr/sbin/pkg_info -e "$p" >/dev/null || /usr/sbin/pkg_add "$p"
	done
	;;
esac
