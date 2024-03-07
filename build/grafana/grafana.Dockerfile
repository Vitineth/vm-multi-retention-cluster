FROM grafana/grafana:10.3.1

ADD victorialogs-download.sh /download.sh
ENTRYPOINT [ "/download.sh" ]