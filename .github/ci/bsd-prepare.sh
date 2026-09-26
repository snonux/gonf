#!/bin/sh
# Install Go and the packages the live BSD package/service tests expect
# (rsync installed; uptimed, or bozohttpd from base on NetBSD).
set -eu
case "$(uname -s)" in
FreeBSD) pkg install -y go git rsync uptimed ;;
OpenBSD) pkg_add go git rsync-- uptimed ;;
NetBSD) /usr/sbin/pkg_add go git rsync ;;
esac
