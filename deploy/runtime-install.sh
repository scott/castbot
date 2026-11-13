#!/bin/sh
# Pins for the shared runtime image. URL artifacts are checked with SHA256.
# Apt packages are pinned to the bookworm versions current on 12 November 2026.
# Their dependencies follow that snapshot of the Debian archive and are not
# hashed one by one. The script fails closed if a checksum or a pin does not match.
set -eu

apt-get update
apt-get install -y --no-install-recommends \
  ca-certificates='20250419~deb12u2' \
  curl='7.88.1-10+deb12u15' \
  git='1:2.39.5-0+deb12u3' \
  ripgrep='13.0.0-4+b2' \
  build-essential='12.9'
rm -rf /var/lib/apt/lists/*

install_tarball() {
  url=$1
  sum=$2
  file=$3
  curl -fsSL --retry 3 -o "$file" "$url"
  echo "$sum  $file" | sha256sum -c -
}

mkdir -p /opt/castbot/libexec /opt /usr/local

install_tarball \
  https://nodejs.org/dist/v26.13.1/node-v26.13.1-linux-x64.tar.gz \
  518674de2b621728a5e4486f5fb3b7d2900bb3cc4aa97969edaa463db99f9778 \
  /tmp/node.tgz
tar -xzf /tmp/node.tgz -C /opt
rm -rf /opt/node
mv /opt/node-v26.13.1-linux-x64 /opt/node
rm -f /tmp/node.tgz

install_tarball \
  https://go.dev/dl/go1.27.4.linux-amd64.tar.gz \
  7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840 \
  /tmp/go.tgz
rm -rf /usr/local/go
tar -xzf /tmp/go.tgz -C /usr/local
rm -f /tmp/go.tgz

install_tarball \
  https://github.com/cli/cli/releases/download/v2.104.0/gh_2.104.0_linux_amd64.tar.gz \
  98cb101ad87dbbd3f6b2f2bfcbd43197ccd07643140121d8a9cfaa91eadbc24e \
  /tmp/gh.tgz
tar -xzf /tmp/gh.tgz -C /tmp
install -m 0755 /tmp/gh_2.104.0_linux_amd64/bin/gh /opt/castbot/libexec/gh
rm -rf /tmp/gh.tgz /tmp/gh_2.104.0_linux_amd64

install_tarball \
  https://github.com/digitalocean/doctl/releases/download/v2.4.0/doctl-2.4.0-linux-amd64.tar.gz \
  7027c4cc2738421d5d4391e2f1646bc773de08844cf78fd8c81ae1506fffa172 \
  /tmp/doctl.tgz
tar -xzf /tmp/doctl.tgz -C /tmp
install -m 0755 /tmp/doctl /opt/castbot/libexec/doctl
rm -f /tmp/doctl.tgz /tmp/doctl

chmod -R a+rX /opt/node /usr/local/go /opt/castbot
