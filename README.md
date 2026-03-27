# Log Forwarder Architecture

The log forwarder is a lightweight component used to collect pod logs from the Kubernetes cluster and send them to an external HTTP endpoint. It is composed of a Go based agent deployed inside the cluster and a simple HTTP server that can receive the forwarded logs.

## Helm Deployment

The Helm chart exposes the log forwarder under the `monitoring.logforwarder` section in `values.yaml`. When enabled, a deployment and the required RBAC rules are created. Pod selection, container filtering and the remote endpoint can be customized using the following values:

```yaml
monitoring:
  logforwarder:
    enabled: true           # Enable or disable the deployment
    namespace: ztx5gc        # Namespace from which logs are streamed
    remoteUrl: http://host:port/logs
    labelSelector: ""        # Optional label selector to filter pods
    containerRegex: ""       # Optional regex to filter container names
```

Sample helm charts can be found under [opensource-5g-core](https://github.com/UmakantKulkarni/opensource-5g-core/blob/ztx_demo_aug/helm-chart/templates/logforwarder.yaml)

## Go Agent

The Go program `main.go` watches for pod events using the client-go shared informer. For every matching container it starts a `LogStreamer` which streams the container logs and posts each line to the configured HTTP server.

Key parts of the implementation:

- **HTTPSender** – wraps an `http.Client` and posts log lines to the remote endpoint. It throttles connection errors so that the logs do not fill with repeated failures when the server is unreachable.
- **LogStreamer** – streams logs from a specific container using the Kubernetes API. Any errors while establishing or reading the stream are also throttled.
- **Informer** – watches for pod add/delete events to start or stop individual streamers.

The agent is built with Go modules and requires Kubernetes client-go libraries. It runs inside the cluster with a service account that grants read access to pod logs.

## Python Receiver

`server.py` provides a minimal reference implementation of an HTTP endpoint that stores received log lines in `received_logs.txt`. It overrides the standard `BaseHTTPRequestHandler` logging so that incoming requests do not clutter the console output.

## Sequence

1. The Helm chart deploys the Go agent.
2. The agent discovers pods based on the provided selector and starts streaming container logs.
3. Each log line is posted to the configured HTTP endpoint.
4. The Python server stores the logs for later inspection.

This mechanism makes it easy to integrate custom log analysis or archival components by simply replacing the receiving server.

## Citation

If you use this tool, we request you to please cite our paper:

**Plain text (ACM-style):**  
Umakant Kulkarni and Sonia Fahmy. 2026. Janus: A Dual-Mask Attention Transformer for Log-based Anomaly Detection in Cellular Networks. Proc. ACM Meas. Anal. Comput. Syst. 10, 1, Article 14 (March 2026), 36 pages. https://doi.org/10.1145/3788096

**BibTeX:**
```bibtex
@article{10.1145/3788096,
author = {Kulkarni, Umakant and Fahmy, Sonia},
title = {Janus: A Dual-Mask Attention Transformer for Log-based Anomaly Detection in Cellular Networks},
year = {2026},
issue_date = {March 2026},
publisher = {Association for Computing Machinery},
address = {New York, NY, USA},
volume = {10},
number = {1},
url = {https://doi.org/10.1145/3788096},
doi = {10.1145/3788096},
journal = {Proc. ACM Meas. Anal. Comput. Syst.},
month = mar,
articleno = {14},
numpages = {36},
keywords = {anomaly detection, cellular networks, attention, llm, masking, system logs, transformers}
}
```

---
