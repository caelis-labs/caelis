FROM busybox:1.36.1@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662

LABEL com.caelis.eval.component="terminalbench-uv-cache"

ADD --checksum=sha256:9db0c2f6683099f86bfeea47f4134e915f382512278de95b2a0e625957594ff3 \
    https://github.com/astral-sh/uv/releases/download/0.9.5/uv-aarch64-unknown-linux-gnu.tar.gz \
    /srv/uv/uv-aarch64-unknown-linux-gnu.tar.gz
ADD --checksum=sha256:42b9b83933a289fe9c0e48f4973dee49ce0dfb95e19ea0b525ca0dbca3bce71f \
    https://github.com/astral-sh/uv/releases/download/0.9.5/uv-aarch64-unknown-linux-musl.tar.gz \
    /srv/uv/uv-aarch64-unknown-linux-musl.tar.gz
ADD --checksum=sha256:2cf10babba653310606f8b49876cfb679928669e7ddaa1fb41fb00ce73e64f66 \
    https://github.com/astral-sh/uv/releases/download/0.9.5/uv-x86_64-unknown-linux-gnu.tar.gz \
    /srv/uv/uv-x86_64-unknown-linux-gnu.tar.gz
ADD --checksum=sha256:3665ffb6c429c31ad6c778ac0489b7746e691acf025cf530b3510b2f9b1660ff \
    https://github.com/astral-sh/uv/releases/download/0.9.5/uv-x86_64-unknown-linux-musl.tar.gz \
    /srv/uv/uv-x86_64-unknown-linux-musl.tar.gz
RUN chmod 0444 /srv/uv/*.tar.gz

EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=2s --start-period=1s --retries=15 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/uv-x86_64-unknown-linux-gnu.tar.gz || exit 1

USER 65534:65534
ENTRYPOINT ["httpd", "-f", "-p", "8080", "-h", "/srv/uv"]
