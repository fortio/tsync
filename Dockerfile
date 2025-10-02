FROM scratch
COPY tsync /usr/bin/tsync
ENV HOME=/home/user
# So -v ~/.tsync:/home/user/.tsync uses the same key as host.
VOLUME ["/home/user/.tsync"]
ENTRYPOINT ["/usr/bin/tsync"]
