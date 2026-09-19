#!/bin/bash
# Generate the certificate and credential the secured lab cache presents.
#
# The plain caches in compose.yaml are http with no credential, which is right
# for what they are: a LAN cache of public images, speeding the matrix up. They
# exercise registry.mirrors and nothing else.
#
# registry.mode external is the other half of the schema, and it is the half a
# customer with a Harbor actually uses: containerd is handed a CA to trust and
# a credential to present. Neither is reachable over http -- the code that
# writes registries.yaml only configures https endpoints -- so verifying that
# path needs a cache that speaks TLS and demands a login. This makes one.
#
# The material is short-lived and disposable. It is regenerated rather than
# kept, for the reason test/lab/material_test.go generates its chain: fixtures
# on disk expire, and a lab that starts failing for a reason nobody changed is
# a lab people stop believing.
#
#   ./mkmaterial.sh 192.168.88.253
#
set -euo pipefail

cd "$(dirname "$0")"

# The certificate is bound to the address the nodes dial. Guessing it produces
# a cache that works from here and fails from every node, which is the most
# expensive kind of wrong, so it is required rather than defaulted.
addr="${1:-}"
if [ -z "$addr" ]; then
	echo "usage: $0 <address the nodes reach this cache at>" >&2
	echo "  e.g. $0 192.168.88.253" >&2
	exit 2
fi

force="${2:-}"
if [ -e tls/registry.key ] && [ "$force" != "--force" ]; then
	echo "material already exists in $(pwd)/tls." >&2
	echo "the running cache is serving it; replacing it needs the cache restarted." >&2
	echo "re-run with --force to replace it anyway." >&2
	exit 3
fi

# An address is a subjectAltName of one kind or the other, and a certificate
# carrying the wrong kind fails verification with a message about the name.
if printf '%s' "$addr" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$'; then
	san="IP:$addr"
else
	san="DNS:$addr"
fi

mkdir -p tls auth
umask 077

echo "==> CA"
openssl req -x509 -newkey rsa:2048 -nodes \
	-keyout tls/ca.key -out tls/ca.crt -days 90 \
	-subj "/CN=Malmok Lab Cache CA" \
	-addext "basicConstraints=critical,CA:TRUE" \
	-addext "keyUsage=critical,keyCertSign,cRLSign"
echo "==> server certificate for $addr ($san)"
openssl req -newkey rsa:2048 -nodes \
	-keyout tls/registry.key -out tls/registry.csr \
	-subj "/CN=$addr"
cat > tls/registry.ext <<EXT
basicConstraints=CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=$san
EXT

openssl x509 -req -in tls/registry.csr \
	-CA tls/ca.crt -CAkey tls/ca.key -CAcreateserial \
	-out tls/registry.crt -days 90 \
	-extfile tls/registry.ext
rm -f tls/registry.csr tls/registry.ext tls/ca.srl

# The registry reads the certificate and key as the user inside the container,
# which is not this one. The key stays unreadable to other users on this host;
# the certificate is public by definition.
chmod 0644 tls/ca.crt tls/registry.crt
chmod 0600 tls/ca.key tls/registry.key

echo "==> credential"
user="malmok"
# hex, so the value survives a YAML document, a shell and an env var without
# quoting rules deciding what it means.
pass="$(openssl rand -hex 24)"

# registry:2 reads bcrypt only. htpasswd is not installed here and this repo
# builds in containers anyway.
docker run --rm httpd:2-alpine htpasswd -Bbn "$user" "$pass" > auth/htpasswd
chmod 0644 auth/htpasswd

# The password has to be readable by whoever runs the build, because the
# document names it as env://REGISTRY_PASSWORD. It is written, never printed:
# this output goes into a terminal, a transcript and possibly a report.
printf '%s' "$user" > auth/username.txt
printf '%s' "$pass" > auth/password.txt
chmod 0600 auth/username.txt auth/password.txt

echo
echo "wrote:"
echo "  tls/ca.crt            the CA a node must trust"
echo "  tls/registry.crt/.key what the cache presents"
echo "  auth/htpasswd         the credential the cache demands"
echo "  auth/username.txt     the username, for the document"
echo "  auth/password.txt     the password, for the document (not printed)"
echo
echo "all of it is ignored by git."
