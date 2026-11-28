# BIND 9 from Alpine, for hosts that cannot run the amd64-only ISC image
# (BIND crashes under qemu, e.g. on arm64 without Rosetta). compose.yml
# builds it when BIND_IMAGE names an image that cannot be pulled. Takes the
# same arguments as the ISC image; the zone files of secondary zones go to
# /var/cache/bind as there.
FROM docker.io/library/alpine:3.24.3
RUN apk add --no-cache bind bind-tools \
  && install -d -o named -g named /var/cache/bind
ENTRYPOINT ["/usr/sbin/named", "-u", "named"]
CMD ["-g", "-c", "/etc/bind/named.conf"]
