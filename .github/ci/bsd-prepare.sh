#!/bin/sh
# Install Go and what the live BSD package/service tests expect (rsync;
# uptimed, or bozohttpd from base on NetBSD). Packages the VM image already
# has are left alone: upgrading them can fail on a dependency skew. uptimed
# is enabled and started up front, as it is on the fleet hosts.
set -eu
case "$(uname -s)" in
FreeBSD)
	for p in go git rsync uptimed; do
		pkg info -e "$p" || pkg install -y "$p"
	done
	sysrc uptimed_enable=YES
	service uptimed status || service uptimed start
	;;
OpenBSD)
	for p in go git rsync uptimed; do
		pkg_info -e "$p-*" >/dev/null || pkg_add "$p" || pkg_add "$p--"
	done
	rcctl enable uptimed
	rcctl check uptimed || rcctl start uptimed
	;;
NetBSD)
	# No git: its pcre2 dependency is newer than the image's and pkg_add
	# will not upgrade it; bsd-test.sh turns off VCS stamping instead.
	for p in go rsync; do
		/usr/sbin/pkg_info -e "$p" >/dev/null || /usr/sbin/pkg_add "$p"
	done
	# The live service tests drive base httpd (bozohttpd) here; see
	# GONF_LIVE_SERVICE in bsd-test.sh.
	mkdir -p /var/www
	;;
esac
# The live service tests expect a settled daemon, as on the fleet; right
# after a start the rc status check can still miss its pid file.
sleep 3
