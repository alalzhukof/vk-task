#!/bin/sh
set -eu
iptables -F INPUT
iptables -F OUTPUT
iptables -P INPUT DROP
iptables -P OUTPUT DROP
iptables -P FORWARD DROP
iptables -A INPUT -i lo -j ACCEPT
iptables -A OUTPUT -o lo -j ACCEPT
iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
# Docker DNS is local to this namespace. No external egress is allowed.
for proto in udp tcp; do
  iptables -A OUTPUT -d 127.0.0.11 -p "$proto" --dport 53 -j ACCEPT
done
for ip in 172.29.20.10 172.29.20.11; do
  iptables -A OUTPUT -d "$ip" -p tcp --dport 5432 -j ACCEPT
done
if [ "$ROLE" = api ]; then
  iptables -A INPUT -s 172.29.10.10 -p tcp --dport 8000 -j ACCEPT
  iptables -A OUTPUT -d 172.29.30.10 -p tcp --dport 6379 -j ACCEPT
else
  for ip in 172.29.40.11 172.29.40.12 172.29.40.13; do
    iptables -A OUTPUT -d "$ip" -p tcp --dport 5672 -j ACCEPT
  done
fi
touch /tmp/ready
exec tail -f /dev/null
