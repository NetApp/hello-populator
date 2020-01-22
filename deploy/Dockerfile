
FROM busybox:1.31.1

ADD deploy/ca-certificates.crt /etc/ssl/certs/
ADD bin/hello-populator /

ENTRYPOINT ["/hello-populator"]
