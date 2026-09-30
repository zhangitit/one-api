#!/bin/sh
set -eu

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
HTTPD_CONFIG=/etc/apache2/httpd.conf
VHOST_DIR=/etc/apache2/vhosts

if ! grep -q '^LoadModule proxy_http_module ' "$HTTPD_CONFIG"; then
    sed -i '' 's|^#LoadModule proxy_http_module |LoadModule proxy_http_module |' "$HTTPD_CONFIG"
fi

install -m 0644 "$SOURCE_DIR/api.kevin.com.conf" "$VHOST_DIR/api.kevin.com.conf"
install -m 0644 "$SOURCE_DIR/compute.kevin.com.conf" "$VHOST_DIR/compute.kevin.com.conf"

/usr/sbin/httpd -t
/usr/sbin/apachectl graceful
