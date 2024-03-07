​	

# Creating a Multi Retention Period VictoriaMetrics Cluster

After a few days of attempts, I finally managed to get my own VictoriaMetrics cluster working and have decided to write up the various components and setups for my own reference and in case its useful to anyone else. The majority of this was pieced together from their example docker deployments in their repo, and some random articles around the web, and their primary documentation.

This setup is far from perfect and there will be updated and refinements to it as I go and interact with it. This is especially true of the final `vmauth` piece, as the set of url mappings is currently very limited and unlikely to support all uses. 

> The code blocks in this post will be sections from the docker-compose file with some modifications. It will usually just be the services portion so networks and volumes will be omitted. You can find the full compose file as part of the associated git repository. 

## Storage

Due to limitations in VictoriaMetrics storage nodes, retention filtering on a label basis is only available with VictoriaMetrics Enterprise (https://docs.victoriametrics.com/#retention-filters). The recommended solution for multiple retention periods using the community edition is therefore to host a cluster using different storage nodes. Each `vmstorage` component manages the storage of metrics, and can be configured with a single retention period. Therefore, for my setup I wanted 3 retention periods (30 days, 6 months, indefinite) which means 3 `vmstorage` cluster nodes. 

```yaml
# Storage Nodes
    vmstorage-30d:
      image: victoriametrics/vmstorage:v1.93.11-cluster
      command:
        - "-retentionPeriod=30d"
        - "-storageDataPath=/storage"
      expose:
        - 8400
      networks:
        - vm_net
      volumes:
        - vmstorage-30d:/storage
      restart: always
      labels:
        "docker_compose_diagram.cluster": "storage"
    
    vmstorage-6mo:
      image: victoriametrics/vmstorage:v1.93.11-cluster
      command:
        - "-retentionPeriod=90d"
        - "-storageDataPath=/storage"
      expose:
        - 8400
      networks:
        - vm_net
      volumes:
        - vmstorage-6mo:/storage
      restart: always

    vmstorage-infd:
      image: victoriametrics/vmstorage:v1.93.11-cluster
      command:
        - "-retentionPeriod=100y"
        - "-storageDataPath=/storage"
      expose:
        - 8400
      networks:
        - vm_net
      volumes:
        - vmstorage-infd:/storage
      restart: always
```

One thing to note is that the storage nodes also don't support infinite retention, therefore for the indefinite storage we use an arbitrarily long retention (in this case 100 years).

## Insertion

Each storage node also needs a linked insertion node which handles receiving metrics and pushing them into storage. `vminsert` nodes can only point to a single storage node, so again you need three insert nodes, each one pointing to its relevant storage node. 

```yaml
    # Insertion Nodes
    vminsert-30d:
      image: victoriametrics/vminsert:v1.93.11-cluster
      depends_on:
        - vmstorage-30d
      command:
        - "-storageNode=\"vmstorage-30d:8400\""
      expose:
        - 8480
      networks:
        - vm_net
      restart: always
    
    vminsert-6mo:
      image: victoriametrics/vminsert:v1.93.11-cluster
      depends_on:
        - vmstorage-6mo
      command:
        - "-storageNode=vmstorage-6mo"
      expose:
        - 8480
      networks:
        - vm_net
      restart: always
    
    vminsert-infd:
      image: victoriametrics/vminsert:v1.93.11-cluster
      depends_on:
        - vmstorage-infd
      command:
        - "-storageNode=vmstorage-infd"
      expose:
        - 8480
      networks:
        - vm_net
      restart: always
```

## Routing

Now that there are three insertion nodes, we need a way to abstract over the top of them so that clients don't have to manually figure out which insertion node to target for specific metrics. This is done via `vmagent` which handles routing writes to an insert node, and also the standard Prometheus scraping functionality. 

A `vmagent` can be configured to have any number of remote write endpoints. By default, it will attempt to share evenly over these urls, switching if any of them become unavailable and generally trying to even the load. However, you can specify relabelling configs on a per url basis. As part of the relabelling configurations, you can choose which metrics to keep and to drop which is how we make this retention system work. 

```yaml
    # Agent
    vmagent:
      image: victoriametrics/vmagent:v1.93.11
      depends_on:
        - vminsert-30d
        - vminsert-6mo
        - vminsert-infd
      ports:
        - 8429:8429
      expose:
        - 8429
      command:
        - "--promscrape.config=/etc/prometheus/prometheus.yml"
        - "-remoteWrite.showURL"
        - "-remoteWrite.url=http://vminsert-30d:8480/insert/0/prometheus/api/v1/write"
        - "-remoteWrite.urlRelabelConfig=/conf/30d-rewrite.conf"
        - "-remoteWrite.url=http://vminsert-6mo:8480/insert/0/prometheus/api/v1/write"
        - "-remoteWrite.urlRelabelConfig=/conf/6mo-rewrite.conf"
        - "-remoteWrite.url=http://vminsert-infd:8480/insert/0/prometheus/api/v1/write"
        - "-remoteWrite.urlRelabelConfig=/conf/infd-rewrite.conf"
      networks:
        - vm_net
      volumes:
        - ./config/vmagent/cluster-metric-ingestion.yml:/etc/prometheus/prometheus.yml
        - ./config/vmagent/conf:/conf
      restart: always
```

This setup has a few more command sections, the `showURL` is used to make the logs show the actual urls its targeting. This isn't required and was a holdover from debugging when I couldn't figure out why it was failing to use the VictoriaMetrics API for sending metrics. This makes use of 3 configuration files, one rewrite config per insertion node

```yaml
# 30d-rewrite.conf
- action: drop
  source_labels: [retention]
  regex: "6mo"
- action: drop
  source_labels: [retention]
  regex: "infd"
```

```yaml
# 6mo-rewrite.conf
- if: '{retention!="6mo"}'
  action: drop
```

```yaml
# infd-rewrite.conf
- if: '{retention!="infd"}'
  action: drop
```

In summary, for the 6 month and indefinite storage, drop all metrics unless they are specifically labelled with a matching retention period. For the 30 day node, we should drop any metrics with an explicit retention period that would be matched by another node, but accept everything else. This ensures that metrics with configured retention periods end up where they are meant to, and those that don't end up in 30 day storage. This is designed to keep data usage down in the case of a misconfigured metric until it can be fixed and routed to the right place. 

As mentioned before, `vmagent` also handles the default prometheus scraping tasks so this is how we ingest metrics from the rest of the project to provide the backing data for the VictoriaMetrics dashboards. You should note that this is quite a lot of metrics so configure it as you please. 

```yaml
# cluster-metric-ingestion.yml
global:
  scrape_interval: 30s

scrape_configs:
  - job_name: 'vmagent'
    static_configs:
      - targets: ['vmagent:8429']
        labels:
          retention: '6mo'
  - job_name: 'vmalert'
    static_configs:
      - targets: ['vmalert:8880']
        labels:
          retention: '6mo'
  - job_name: 'vminsert'
    static_configs:
      - targets: ['vminsert-30d:8480', 'vminsert-6mo:8480', 'vminsert-infd:8480']
        labels:
          retention: '6mo'
  - job_name: 'vmselect'
    static_configs:
      - targets: ['vmselect:8481']
        labels:
          retention: '6mo'
  - job_name: 'vmstorage'
    static_configs:
      - targets: ['vmstorage-30d:8482', 'vmstorage-6mo:8482', 'vmstorage-infd:8482']
        labels:
          retention: '6mo'
  - job_name: 'vmlogs'
    static_configs:
      - targets: ['victorialogs:9428']
        labels:
          retention: '6mo'
  - job_name: 'alertmanager'
    static_configs:
      - targets: ['alertmanager:9093']
        labels:
          retention: '6mo'
  - job_name: 'vmauth'
    static_configs:
      - targets: ['vmauth:8427']
        labels:
          retention: '6mo'
```

As you can see, this also uses the label configs to force each metric from these scrapes to be routed to the right storage node. In this case I've chosen 6 months because the overview metrics here can be quite useful, but there are a lot of metrics here that I don't really care about. I want to be able to see how the system is changing over time, but not necessarily all the way back to its creation. If you want to reduce the amount of datapoints being ingested this way, my recommendation is to increase the `scrape_interval`. 

## Querying

Now that we have data being ingested into the cluster and routed to its correct storage nodes, we want a way to query the data and actually make it usable. This is the role of `vmselect` (unsurprisingly), and thankfully, it supports any number of storage nodes which means we can connect it up to all the storage nodes in one go

```yaml
    vmselect:
      image: victoriametrics/vmselect:v1.93.11-cluster
      depends_on:
        - vmstorage-30d
        - vmstorage-6mo
        - vmstorage-infd
      command:
        - "-storageNode=vmstorage-30d"
        - "-storageNode=vmstorage-6mo"
        - "-storageNode=vmstorage-infd"
      ports:
        - "8481:8481"
      expose:
        - 8481
      networks:
        - vm_net
      restart: always
```

## Alerting

VictoriaMetrics also ships with a full alerting tool that integrates with Prometheus' AlertManager tool for paging / notifying. Alerting is somewhat unique in this setup in that it requires being able to read from and write to the metrics cluster (as it tracks running alerts via `ALERTS` and `ALERTS_FOR_STATE`). 

```yaml
    # Alerting
    vmalert:
      image: victoriametrics/vmalert:v1.97.1
      depends_on:
        - "vmstorage-30d"
        - "vmstorage-6mo"
        - "vmstorage-infd"
        - "vmagent"
        - "alertmanager"
      ports:
        - 8880:8880
      volumes:
        - ./config/vmalert/alerts:/etc/alerts
      command:
        - "--datasource.url=http://vmauth:8427/select/0/prometheus/"
        - "--remoteRead.url=http://vmauth:8427/select/0/prometheus/"
        - "--remoteWrite.url=http://vminsert-6mo:8480/insert/0/prometheus/"
        - "--notifier.url=http://alertmanager:9093/"
        - "--rule=/etc/alerts/*.yml"
        - "--external.url=http://172.0.0.1:3000"
        - '--external.alert.source=explore?orgId=1&left={"datasource":"VictoriaMetrics","queries":[{"expr":{{$$expr|jsonEscape|queryEscape}},"refId":"A"}],"range":{"from":"now-1h","to":"now"}}'
      networks:
        - vm_net
      restart: always

    alertmanager:
      # image: prom/alertmanager:v0.27.0
      build:
        context: ./build/alertmanager
        dockerfile: alertmanager.Dockerfile
      ports:
        - 9093:9093
      volumes:
        - ./config/alertmanager/alertmanager.yml:/config/alertmanager.yml
        - ./config/alertmanager/rehook.pem:/config/rehook.pem
      networks:
        - vm_net
      env_file:
        - ./build/alertmanager/rehook.env
      restart: always
```

So `vmalert` handles running the actual alerts (hence the volume mount in `/etc/alerts`). It will run the alerts against the provided `datasource.url`/`remoteRead.url`, and then write the metrics back to `remoteWrite.url`. When an alert is actually firing, it will send the alerts to `notifier.url` which is handled by the `alertmanager` instance. You should configure the `external.url` value to an accurate value, currently mine is just set to localhost (incorrectly, whoops). 

AlertManager handles the actual notification system, the reference to `rehook` here is a custom tool written which receives webhooks from AlertManager and sends them into my own custom notification system which ultimately ends up with them being delivered to my phone. Due to some auth issues this had to be implemented as its own thing which is why we use a custom build image. The image runs alertmanager and the rehook webhook server simultaneously using `supervisord`. 

## Logging

VictoriaMetrics also has support for log management via their new `victorialogs` and query language `LogML`. For scraping the logs from docker, we use `flutentbit` which will then route the logs into `victorialogs` for storage and querying ability. 

```yaml
    # Logging
    fluentbit:
      image: cr.fluentbit.io/fluent/fluent-bit:2.1.4
      volumes:
        - /var/lib/docker/containers:/var/lib/docker/containers:ro
        - ./config/logs/fluent-bit.conf:/fluent-bit/etc/fluent-bit.conf
      depends_on: [victorialogs]
      ports:
        - "5140:5140"
      networks:
        - vm_net

    victorialogs:
      image: docker.io/victoriametrics/victoria-logs:v0.5.0-victorialogs
      command:
        - "--storageDataPath=/vlogs"
        - "--httpListenAddr=:9428"
      volumes:
        - vmlogs:/vlogs
      ports:
        - "9428:9428"
      networks:
        - vm_net
```

And fluentbit is configured to parse the json and push it to `victorialogs`

```ini
# fluent-bit.conf
[INPUT]
    name              tail
    path              /var/lib/docker/containers/**/*.log
    path_key         path
    multiline.parser  docker, cri
    Parser docker
    Docker_Mode  On

[INPUT]
    Name     syslog
    Listen   0.0.0.0
    Port     5140
    Parser   syslog-rfc3164
    Mode     tcp

[SERVICE]
    Flush        1
    Parsers_File parsers.conf
    HTTP_Server  On
    HTTP_Listen  0.0.0.0
    HTTP_PORT    2020

[Output]
    Name http
    Match *
    host victorialogs
    port 9428
    compress gzip
    uri /insert/jsonline?_stream_fields=stream,path&_msg_field=log&_time_field=date
    format json_lines
    json_date_format iso8601
    header AccountID 0
    header ProjectID 0
```

## Visualisation

Finally, we use trusty Grafana for visualisation of the metrics and the logs via VictoriaMetrics custom data source. To make this work, we need to use a custom build that adds the script the provision the datasource on launch. 

```yaml
    # Grafana
    grafana:
      build:
        context: ./build/grafana
        dockerfile: grafana.Dockerfile
      depends_on:
        - "vmagent"
        - "vmselect"
        - "victorialogs"
      ports:
        - 3000:3000
      restart: always
      command: [ "chmod +x /download.sh && /download.sh && /run.sh" ]
      volumes:
        - grafana_data:/var/lib/grafana
        - ./config/grafana/provisioning:/etc/grafana/provisioning
        - ./config/grafana/dashboards:/var/lib/grafana/dashboards
        - ./config/grafana/grafana.ini:/etc/grafana/grafana.ini
      networks:
        - vm_net
```

We also provide a set of pre-made dashboards for monitoring the VictoriaMetrics cluster, provided by the team. The launch command is overwritten to run the download and provisioning script for the `victorialogs` data source on launch. This is also a nop if its already installed. 

Due to some issues with how grafana handles permissions, bind mounts tend to be difficult to manage which is why this uses a docker volume for its storage. 

## Routing

Finally, as a last minute addition, I wanted to add `vmauth` as a http proxy in front of all the components to provide a single endpoint with which to interact with the system. This is still a work in progress as I find endpoints that I need and have missed. 

```yaml
    # Routing
    vmauth:
      image: victoriametrics/vmauth:v1.99.0
      depends_on:
        - vmagent
        - vmselect
      command:
        - "--auth.config=/etc/auth.yml"
      volumes:
        - ./config/vmauth/auth.yml:/etc/auth.yml
      ports:
        - 8427:8427
      networks:
        - vm_net
```

And this uses a very, very basic config that needs expanding

```yaml
# auth.yml
unauthorized_user:
  url_map:
  - src_paths:
    - "/api/v1/write(/.*)?"
    url_prefix:
    - "http://vmagent:8429/"
  - src_paths:
    - "/select/.+"
    url_prefix:
    - "http://vmselect:8481/"
```

## Summary

All in all, this produces 14 containers which all actually work together to produce a nice installation. This is being run locally right now but generally seems to be working well. The routing, as far as I can tell, seems to be working as expected by monitoring the bytes used and correlating it with when data sources started writing to it. This is still very much in progress, and my data sources are incredibly limited (right now its just the cluster and monitoring the temperatures on my 3d printer as a proof of concept). I might come back and update this document as I work on it and fix different bits but will generally try and keep the git repository up to date. 

If you spot any errors in this configuration, or ways this could be improved, please let me know!

