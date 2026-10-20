FROM golang:1.26-bookworm
ENV DEBIAN_FRONTEND=noninteractive
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources \
 && apt-get -o Acquire::Retries=1 -o Acquire::https::Timeout=20 -o APT::Update::Error-Mode=any update \
 && apt-get install -y --no-install-recommends iproute2 wireguard-tools iperf3 iptables iputils-ping python3 curl procps
WORKDIR /lab
