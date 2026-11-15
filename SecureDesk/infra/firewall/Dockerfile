FROM alpine:3.21@sha256:bc21487101ae47172509130ee1092cc273a831968d64b617776d1f69c81e42ab
RUN apk add --no-cache iptables iproute2
COPY rules.sh /rules.sh
CMD ["sh", "/rules.sh"]
