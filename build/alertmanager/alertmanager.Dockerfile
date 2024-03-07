FROM golang:1.21
WORKDIR /src
ENV CGO_ENABLE=0
ADD rehook /src/
RUN go get
RUN go build -o /bin/rehook ./main.go

FROM debian:latest

RUN apt-get update
RUN apt-get install -y ca-certificates
COPY --from=prom/alertmanager:v0.27.0 /bin/alertmanager /bin/alertmanager
COPY --from=ochinchina/supervisord:latest /usr/local/bin/supervisord /usr/local/bin/supervisord
COPY supervisord.conf /etc/supervisor/supervisord.conf
COPY --from=0 /bin/rehook /bin/rehook
ENTRYPOINT ["/usr/local/bin/supervisord"]