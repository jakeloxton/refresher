# Refresher

![Docker Build](https://github.com/jakeloxton/refresher/actions/workflows/docker-image.yml/badge.svg)

Refresher is a tool to reload a Kubernetes deployment when the contents of a URL change. Configuration is provided by a key=value pairing, the key being the ID to annotate your deployments with, the value being the URL to watch. By default no reloads are triggered until the URL has been verified three times.

Similiar concept to [jimmidyson/configmap-reload](https://github.com/jimmidyson/configmap-reload) but for URLs, not configmaps.

Mainly useful for when applications retrieve their configuration from a URL on startup.

## Usage

```
  Usage: refresher [options]

  Options:
  --check-time, -c          how often to run the check in seconds (default 10ns, env CHECK_TIME)
  --production-logging, -p  sets whether production logging is on (json, env PRODUCTION_LOGGING)
  --sources-file, -s        sets the filepath of the sources key=value file (default
                            /etc/refresher/refresher.conf, env SOURCES_FILE)
  --check-threshold         how many times changes should be verified before a reload is triggered
                            (default 3, env CHECK_THRESHOLD)
  --annotation-key, -a      the annotation key to be checked when a reload is triggered (default
                            refresher.mrl/source, env ANNOTATION_KEY)
  --reload-key, -r          the annotation key to trigger a reload (default refresher.mrl/reloaded-at,
                            env RELOAD_KEY)
  --help, -h                display help
```

## Installation

Edit the **deploy/manifest.yaml** file to provide a key=value list of URLs to check.
Example:

```
kind: ConfigMap 
apiVersion: v1 
metadata:
  name: refresher-config
  namespace: default
data:
  refresher.conf: |
    config-api=http://config-api:8080/config
    test-api=http://test-api:7000/test
```

Deploy the **deploy/manifest.yaml** file to your Kubernetes cluster.

```
kubectl apply -f deploy/manifest.yaml
```

Label any deployments to be reloaded with the key matching the URL given in the configmap:

```
kubectl annotate deploy <deployment> refresher.mrl/source=config-api
```

## Known Limitations

- No way to pass headers (e.g. API keys)
- No custom TLS support
- No support for daemonsets or statefulsets
- Watches cluster wide, can't be limited to a namespace
