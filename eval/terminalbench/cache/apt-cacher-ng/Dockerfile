FROM debian:bookworm-slim@sha256:b1a741487078b369e78119849663d7f1a5341ef2768798f7b7406c4240f86aef

ENV DEBIAN_FRONTEND=noninteractive
LABEL com.caelis.eval.component="terminalbench-apt-cache"

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        apt-cacher-ng \
        ca-certificates \
        curl \
    && rm -rf /var/lib/apt/lists/*

VOLUME ["/var/cache/apt-cacher-ng"]
EXPOSE 3142

HEALTHCHECK --interval=2s --timeout=2s --start-period=2s --retries=15 \
    CMD curl --fail --silent --show-error http://127.0.0.1:3142/acng-report.html >/dev/null || exit 1

USER apt-cacher-ng
ENTRYPOINT ["/usr/sbin/apt-cacher-ng", "-c", "/etc/apt-cacher-ng"]
CMD ["ForeGround=1"]
