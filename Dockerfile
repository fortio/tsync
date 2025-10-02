FROM scratch
COPY tsync /usr/bin/tsync
ENTRYPOINT ["/usr/bin/tsync"]
